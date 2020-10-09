package beacon

import (
	"fmt"
	"net"
)

// Traceroute between specified source and destination devices
// sourceIP needs to be provided if source device is in a different Autonomous System and source IP cannot be determined automatically
func Traceroute(destinationIP string, sourceIP string, timeout int, interfaceDevice string) ([]string, error) {

	var destIP net.IP = net.ParseIP(destinationIP)

	route := make([]string, 0)

	destHostname, err := net.LookupAddr(destIP.String())
	if err != nil {
		fmt.Printf("Doing traceroute to %s\n", destIP)
	} else {
		fmt.Printf("Doing traceroute to %s (%s)\n", destHostname[0], destIP)
	}

	if interfaceDevice == "" {
		discoveredOutboundInterface, err := GetInterfaceDeviceFromDestIP(destIP)
		if err != nil {
			return nil, fmt.Errorf("Failed to find an interface for %s: %s, explicitly provide an interface with -i", destIP.String(), err)
		}
		interfaceDevice = discoveredOutboundInterface
	}

	tc, err := NewTransportChannel(
		WithBPFFilter("icmp"),
		WithInterface(interfaceDevice),
		WithTimeout(100),
	)
	if err != nil {
		return nil, fmt.Errorf("Error creating transport channel: %s", err)
	}

	var srcIP net.IP = nil
	if len(sourceIP) > 0 {
		srcIP = net.ParseIP(sourceIP)
	}

	pc, err := tc.GetPathChannelTo(destIP, srcIP, timeout)
	if err != nil {
		return nil, err
	}

	hopIdx := 1
	for hop := range pc {
		fmt.Printf("%d: ", hopIdx)
		hopIdx++

		if hop == nil {
			fmt.Println("*")
			route = append(route, "*")
			continue
		}

		hostname, err := net.LookupAddr(hop.String())
		if err != nil {
			fmt.Println(hop.String())
			route = append(route, hop.String())

		} else {
			fmt.Printf("%s (%s)\n", hostname[0], hop.String())
			route = append(route, fmt.Sprintf("%s (%s)\n", hostname[0], hop.String()))
		}
	}

	return route, nil
}
