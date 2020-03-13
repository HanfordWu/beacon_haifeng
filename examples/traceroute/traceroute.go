package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/trstruth/beacon"
)

func main() {
	destIP := net.ParseIP(os.Args[1])

	err := Traceroute(destIP)
	if err != nil {
		log.Fatal(err)
	}
}

// Traceroute performs traditional traceroute
func Traceroute(destIP net.IP) error {
	destHostname, err := net.LookupAddr(destIP.String())
	if err != nil {
		fmt.Printf("Doing traceroute to %s\n", destIP)
	} else {
		fmt.Printf("Doing traceroute to %s (%s)\n", destHostname[0], destIP)
	}

	pc, err := beacon.GetPathChannelTo(destIP)
	if err != nil {
		return err
	}

	for hop := range pc {
		hostname, err := net.LookupAddr(hop.String())
		if err != nil {
			fmt.Println(hop.String())
		} else {
			fmt.Println(hostname[0])
		}
	}

	return nil
}