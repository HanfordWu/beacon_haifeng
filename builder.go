package beacon

import (
	"errors"
	"fmt"
	"net"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func buildIPIPLayer(sourceIP, destIP net.IP, totalLength uint16) *layers.IPv4 {
	ipipLayer := &layers.IPv4{
		Version:  4,
		IHL:      5,
		Length:   totalLength,
		Flags:    layers.IPv4DontFragment,
		TTL:      255,
		Protocol: layers.IPProtocolIPv4,
		SrcIP:    sourceIP,
		DstIP:    destIP,
	}

	return ipipLayer
}

func buildIPv4ICMPLayer(sourceIP, destIP net.IP, totalLength uint16, ttl uint8) *layers.IPv4 {
	ipLayer := &layers.IPv4{
		Version:  4,
		IHL:      5,
		Length:   totalLength,
		Flags:    layers.IPv4DontFragment,
		TTL:      ttl,
		Protocol: layers.IPProtocolICMPv4,
		SrcIP:    sourceIP,
		DstIP:    destIP,
	}

	return ipLayer
}

func buildUDPLayer(sourceIP, destIP net.IP, totalLength uint16) *layers.IPv4 {
	ipLayer := &layers.IPv4{
		Version: 4,
		IHL: 5,
		Length: totalLength,
		Flags: layers.IPv4DontFragment,
		TTL: 255,
		Protocol: layers.IPProtocolUDP,
		SrcIP: sourceIP,
		DstIP: destIP,
	}

	return ipLayer
}

func buildICMPTraceroutePacket(sourceIP, destIP net.IP, ttl uint8, payload []byte, buf gopacket.SerializeBuffer) error {
	opts := gopacket.SerializeOptions{
		ComputeChecksums: true,
	}

	ipLength := uint16(ipHeaderLen + icmpHeaderLen + len(payload))
	ipLayer := buildIPv4ICMPLayer(sourceIP, destIP, ipLength, ttl)

	icmpLayer := &layers.ICMPv4{
		TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0),
		Seq:      1,
	}

	err := gopacket.SerializeLayers(buf, opts,
		ipLayer,
		icmpLayer,
		gopacket.Payload(payload),
	)
	if err != nil {
		return err
	}
	return nil
}

func buildEncapTraceroutePacket(outerSourceIP, outerDestIP, innerSourceIP, innerDestIP net.IP, ttl uint8, payload []byte, buf gopacket.SerializeBuffer) error {
	opts := gopacket.SerializeOptions{
		ComputeChecksums: true,
	}

	ipipLength := uint16(ipHeaderLen + ipHeaderLen + icmpHeaderLen + len(payload))
	ipipLayer := buildIPIPLayer(outerSourceIP, outerDestIP, ipipLength)

	ipLength := uint16(ipHeaderLen + icmpHeaderLen + len(payload))
	ipLayer := buildIPv4ICMPLayer(innerSourceIP, innerDestIP, ipLength, ttl)

	icmpLayer := &layers.ICMPv4{
		TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0),
		Seq:      1,
	}

	err := gopacket.SerializeLayers(buf, opts,
		ipipLayer,
		ipLayer,
		icmpLayer,
		gopacket.Payload(payload),
	)
	if err != nil {
		return err
	}
	return nil
}

// CreateRoundTripPacketForPath builds an IP in IP packet which will perform roundtrip traversal over the hops in the given path
func CreateRoundTripPacketForPath(path Path, payload []byte, buf gopacket.SerializeBuffer) error {
	opts := gopacket.SerializeOptions{
		ComputeChecksums: true,
	}

	if len(path) < 2 {
		return errors.New("Path must have atleast 2 hops")
	}

	numHops := len(path)
	numLayers := 2 * (numHops - 1)
	lenOverhead := len(payload) + udpHeaderLen + ipHeaderLen

	constructedLayers := make([]gopacket.SerializableLayer, numLayers)

	for idx := range path[:len(path)-1] {
		hopA := path[idx]
		hopB := path[idx+1]

		depLen := uint16(ipHeaderLen * (numLayers - idx) + lenOverhead)
		arrLen := uint16(ipHeaderLen * (idx + 1) + lenOverhead)

		constructedLayers[idx] = buildIPIPLayer(hopA, hopB, depLen)
		constructedLayers[numLayers - idx - 1] = buildIPIPLayer(hopB, hopA, arrLen)
	}

	constructedLayers = append(constructedLayers, buildUDPLayer(path[1], path[0], uint16(ipHeaderLen + udpHeaderLen + len(payload))))
	/*
	constructedLayers = append(constructedLayers, &layers.UDP{
		Length: uint16(udpHeaderLen + len(payload)),
		SrcPort: 62003,
		DstPort: 62002,
	})
	*/
	constructedLayers = append(constructedLayers, gopacket.Payload(payload))

	for _, layer := range constructedLayers {
		fmt.Println(layer)
	}

	err := gopacket.SerializeLayers(buf, opts, constructedLayers...)
	if err != nil {
		return err
	}
	return nil
}

