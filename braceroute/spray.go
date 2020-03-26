package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/spf13/cobra"
	"github.com/trstruth/beacon"
)

var source string
var dest string
var timeout int
var numPackets int

// SprayCmd represents the spray subcommand which allows a user to send
// a spray of packets over a path from source to dest
var SprayCmd = &cobra.Command{
	Use:   "spray",
	Short: "spray packets over a path",
	Long:  "longer description for spraying packets over a path",
	RunE:  sprayRun,
}

type boomerangResult struct {
	err       error
	errorType boomerangErrorType
	payload   string
}

type boomerangErrorType int

const (
	timedOut boomerangErrorType = iota
	fatal    boomerangErrorType = iota
)

func initSpray() {
	SprayCmd.Flags().StringVarP(&source, "source", "s", "", "source IP/host (defaults to eth0 interface)")
	SprayCmd.Flags().StringVarP(&dest, "dest", "d", "", "destination IP/host (required)")
	SprayCmd.MarkFlagRequired("dest")
	SprayCmd.Flags().IntVarP(&timeout, "timeout", "t", 3, "time (s) to wait on a packet to return")
	SprayCmd.Flags().IntVarP(&numPackets, "num-packets", "n", 30, "number of packets to spray")
}

func sprayRun(cmd *cobra.Command, args []string) error {
	var err error
	var srcIP, destIP net.IP

	// if no source was provided via cli flag, default to local
	if source == "" {
		srcIP, err = beacon.FindLocalIP()
	} else {
		srcIP, err = beacon.ParseIPFromString(source)
	}
	if err != nil {
		return err
	}

	destIP, err = beacon.ParseIPFromString(dest)
	if err != nil {
		return err
	}

	fmt.Printf("Finding path from %s to %s\n", srcIP, destIP)
	pathFinderTC, err := beacon.NewTransportChannel(
		beacon.WithBPFFilter("icmp"),
		beacon.WithInterface(interfaceDevice),
	)
	if err != nil {
		return err
	}
	path, err := pathFinderTC.GetPathFromSourceToDest(srcIP, destIP)
	if err != nil {
		return err
	}
	pathFinderTC.Close()

	// if the caller isn't the host, prepend the host to the path
	if source != "" {
		vantageIP, err := beacon.FindLocalIP()
		if err != nil {
			return err
		}
		path = append([]net.IP{vantageIP}, path...)
	}
	// path = []net.IP{net.IP{10, 20, 30, 96}, net.IP{207, 46, 35, 118}, net.IP{104, 44, 18, 117}}
	fmt.Printf("%v\n", path)

	tc, err := beacon.NewTransportChannel(beacon.WithBPFFilter("ip proto 4"))
	if err != nil {
		return err
	}

	resultChannels := make([]chan boomerangResult, len(path)-1)
	for i := 2; i <= len(path); i++ {
		resultChannels[i-2] = spray(path[0:i], tc)
	}

	stats := newSprayStats(path)

	handleResult := func(result boomerangResult) error {
		if result.err != nil {
			if result.errorType == fatal {
				return result.err
			} else if result.errorType == timedOut {
				fmt.Printf("TIMED OUT payload is %s\n", string(result.payload))
				stats.recordResponse(string(result.payload), false)
				return nil
			} else {
				return errors.New("Unhandled error type: " + string(result.errorType))
			}
		}
		fmt.Printf("SUCCESS result is %s\n", string(result.payload))
		stats.recordResponse(string(result.payload), true)
		return nil
	}

	for res := range merge(resultChannels...) {
		err := handleResult(res)
		if err != nil {
			return err
		}
		fmt.Println("\033[H\033[2J")
		fmt.Println(stats)
	}

	return nil
}

func merge(resultChannels ...chan boomerangResult) <-chan boomerangResult {
	var wg sync.WaitGroup
	resultChannel := make(chan boomerangResult)

	drain := func(c chan boomerangResult) {
		for res := range c {
			resultChannel <- res
		}
		wg.Done()
	}

	wg.Add(len(resultChannels))
	for _, c := range resultChannels {
		go drain(c)
	}

	go func() {
		wg.Wait()
		close(resultChannel)
	}()

	return resultChannel
}

func spray(path beacon.Path, tc *beacon.TransportChannel) chan boomerangResult {
	payload := []byte(path[len(path)-1].String())
	resultChan := make(chan boomerangResult)

	go func() {
		for i := 1; i <= numPackets; i++ {
			result := <-boomerang(payload, path, timeout)
			resultChan <- result
		}
		close(resultChan)
	}()

	return resultChan
}

func boomerang(payload []byte, path beacon.Path, timeout int) chan boomerangResult {
	seen := make(chan boomerangResult)
	resultChan := make(chan boomerangResult)

	buf := gopacket.NewSerializeBuffer()

	err := beacon.CreateRoundTripPacketForPath(path, payload, buf)
	if err != nil {
		resultChan <- boomerangResult{
			err:       err,
			errorType: fatal,
		}
	}

	tc, err := beacon.NewTransportChannel(beacon.WithBPFFilter("ip proto 4"))
	if err != nil {
		resultChan <- boomerangResult{
			err:       err,
			errorType: fatal,
		}
	}

	go func() {
		for packet := range tc.Rx() {
			udpLayer := packet.Layer(layers.LayerTypeUDP)
			ipv4Layer := packet.Layer(layers.LayerTypeIPv4)
			udp, _ := udpLayer.(*layers.UDP)
			ip4, _ := ipv4Layer.(*layers.IPv4)

			if ip4.DstIP.Equal(path[0]) && ip4.SrcIP.Equal(path[1]) && bytes.Equal(udp.Payload, payload) {
				seen <- boomerangResult{
					payload: string(udp.Payload),
				}
			}
		}
	}()

	go func() {
		timeOutDuration := time.Duration(timeout) * time.Second
		timer := time.NewTimer(timeOutDuration)

		err = tc.SendToPath(buf.Bytes(), path)
		if err != nil {
			resultChan <- boomerangResult{
				err:       err,
				errorType: fatal,
			}
		}

		select {
		case result := <-seen:
			resultChan <- result
		case <-timer.C:
			resultChan <- boomerangResult{
				payload:   path[len(path)-1].String(),
				err:       errors.New("timed out waiting for packet from " + path[len(path)-1].String()),
				errorType: timedOut,
			}
		}

		tc.Close()
	}()

	return resultChan
}
