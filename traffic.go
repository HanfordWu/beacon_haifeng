package beacon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/uuid"
)

// BoomerangResult represents the completion of one run of boomerang, contains information about potential errors
// or the payload of a successful run
type BoomerangResult struct {
	Err       error
	ErrorType BoomerangErrorType
	Payload   BoomerangPayload
}

// BoomerangPayload is a field of BoomerangResult which is only populated when the BoomerangResult did not encounter an error
// this struct is designed to be JSON unmarshalled from the IP payload in the boomerang packet
type BoomerangPayload struct {
	DestIP      net.IP
	ID          string
	TxTimestamp time.Time
	RxTimestamp time.Time
}

// NewBoomerangPayload constructs a BoomerangPayload struct
func NewBoomerangPayload(destIP net.IP, id string) *BoomerangPayload {
	return &BoomerangPayload{
		DestIP: destIP,
		ID:     id,
	}
}

// BoomerangErrorType is an enum of possible errors encountered during a run of boomerang
type BoomerangErrorType int

const (
	timedOut  BoomerangErrorType = iota
	fatal     BoomerangErrorType = iota
	sendError BoomerangErrorType = iota
)

// IsFatal returns true if the error is fatal, otherwise returns false
func (b *BoomerangResult) IsFatal() bool {
	return b.ErrorType == fatal
}

// ProbeEachHopOfPath probes each hop in a path, but accepts a transport channel as an argument.  This allows the caller to share
// one transport channel between many calls to Probe.  The supplied tranport channel must have a BPFFilter of "ip proto 4"
func (tc *TransportChannel) ProbeEachHopOfPath(path Path, numPackets int, timeout int) <-chan BoomerangResult {
	if !strings.Contains(tc.filter, "ip proto 4") {
		resultChan := make(chan BoomerangResult)

		errMsg := fmt.Sprintf("The supplied TransportChannel must have a BPFFilter containing ip proto 4. The supplied filter was: %s", tc.filter)
		resultChan <- BoomerangResult{Err: fmt.Errorf(errMsg), ErrorType: fatal}

		return resultChan
	}

	resultChannels := make([]chan BoomerangResult, len(path)-1)
	for i := 2; i <= len(path); i++ {
		resultChannels[i-2] = tc.Probe(path[0:i], numPackets, timeout)
	}

	return merge(resultChannels...)
}

// ProbeEachHopOfPathSync synchronously probes each hop in a path.  That is, it waits for each round of packets to come
// back from each hop before sending the next round
func (tc *TransportChannel) ProbeEachHopOfPathSync(path Path, numPackets int, timeout int) <-chan BoomerangResult {
	if !strings.Contains(tc.filter, "ip proto 4") {
		resultChan := make(chan BoomerangResult)

		errMsg := fmt.Sprintf("The supplied TransportChannel must have a BPFFilter containing ip proto 4. The supplied filter was: %s", tc.filter)
		resultChan <- BoomerangResult{Err: fmt.Errorf(errMsg), ErrorType: fatal}

		return resultChan
	}

	resultChan := make(chan BoomerangResult)

	go func() {
		defer close(resultChan)
		for packetCount := 1; packetCount <= numPackets; packetCount++ {
			var wg sync.WaitGroup
			wg.Add(len(path) - 1)

			for i := 2; i <= len(path); i++ {
				go func(idx int) {
					resultChan <- tc.Boomerang(path[0:idx], timeout)
					wg.Done()
				}(i)
			}

			wg.Wait()
			time.Sleep(time.Duration(timeout) * time.Second)
		}
	}()

	return resultChan
}

// Probe generates traffic over a given path and returns a channel of boomerang results
func (tc *TransportChannel) Probe(path Path, numPackets int, timeout int) chan BoomerangResult {
	resultChan := make(chan BoomerangResult)

	go func() {
		for i := 1; i <= numPackets; i++ {
			result := tc.Boomerang(path, timeout)
			resultChan <- result
		}
		close(resultChan)
	}()

	return resultChan
}

// Boomerang sends one packet which "boomerangs" over a given path.  For example, if the path is A,B,C,D the packet will travel
// A -> B -> C -> D -> C -> B -> A
func (tc *TransportChannel) Boomerang(path Path, timeout int) BoomerangResult {
	listenerReady := make(chan bool)
	seen := make(chan BoomerangResult)
	resultChan := make(chan BoomerangResult)

	destHop := path[len(path)-1]
	id := uuid.New().String()
	payload, err := json.Marshal(NewBoomerangPayload(destHop, id))
	if err != nil {
		return BoomerangResult{
			Err:       err,
			ErrorType: fatal,
		}
	}

	buf := gopacket.NewSerializeBuffer()
	err = CreateRoundTripPacketForPath(path, payload, buf)
	if err != nil {
		return BoomerangResult{
			Err:       err,
			ErrorType: fatal,
		}
	}

	criteria := func(packet gopacket.Packet, payload *BoomerangPayload) bool {
		ipv4Layer := packet.Layer(layers.LayerTypeIPv4)
		ip4, _ := ipv4Layer.(*layers.IPv4)

		if ip4.DstIP.Equal(path[0]) && ip4.SrcIP.Equal(path[1]) {
			if payload.ID == id {
				return true
			}
		}
		return false
	}

	listener := NewListener(criteria)
	packetMatchChan := tc.RegisterListener(listener)

	go func() {
		listenerReady <- true
		for packet := range packetMatchChan {
			udpLayer := packet.Layer(layers.LayerTypeUDP)
			udp, _ := udpLayer.(*layers.UDP)

			unmarshalledPayload := &BoomerangPayload{}
			json.Unmarshal(udp.Payload, unmarshalledPayload) // handle unmarshal errors
			unmarshalledPayload.RxTimestamp = time.Now().UTC()
			seen <- BoomerangResult{
				Payload: *unmarshalledPayload,
			}
			return
		}
	}()

	// tx goroutine
	go func() {
		<-listenerReady

		timeOutDuration := time.Duration(timeout) * time.Second
		timer := time.NewTimer(timeOutDuration)

		txTime := time.Now().UTC()
		err := tc.SendToPath(buf.Bytes(), path)
		if err != nil {
			fmt.Printf("error in SendToPath: %s\n", err)
			resultChan <- BoomerangResult{
				Err:       err,
				ErrorType: sendError,
				Payload: BoomerangPayload{
					DestIP: path[len(path)-1],
				},
			}
			return
		}

		select {
		case result := <-seen:
			result.Payload.TxTimestamp = txTime
			resultChan <- result
		case <-timer.C:
			tc.UnregisterListener(listener)
			resultChan <- BoomerangResult{
				Payload: BoomerangPayload{
					DestIP:      path[len(path)-1],
					TxTimestamp: txTime,
					RxTimestamp: time.Now().UTC(),
				},
				Err:       errors.New("timed out waiting for packet from " + path[len(path)-1].String()),
				ErrorType: timedOut,
			}
		}
	}()

	return <-resultChan
}
