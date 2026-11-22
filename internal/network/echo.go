package network

import "encoding/binary"

func (gateway *Gateway) forwardEcho(packet []byte, parsed networkPacket) {
	copyPacket := append([]byte(nil), packet...)

	gateway.launch(func() {
		// Re-check at the platform send boundary, independently of frame validation.
		if !gateway.policy.permits(parsed.destination) || !networkDestination(parsed.destination) {
			return
		}

		payload := copyPacket[parsed.header:]

		err := sendNetworkEcho(gateway.ctx, parsed.destination, payload)
		if err != nil {
			return
		}

		payload[2] = 0
		payload[3] = 0

		if parsed.source.Is4() {
			copy(copyPacket[12:16], parsed.destination.AsSlice())
			copy(copyPacket[16:20], parsed.source.AsSlice())
			copyPacket[8] = 64
			copyPacket[10] = 0
			copyPacket[11] = 0

			payload[0] = 0

			binary.BigEndian.PutUint16(payload[2:4], networkChecksum(payload, 0))
			binary.BigEndian.PutUint16(copyPacket[10:12], networkChecksum(copyPacket[:20], 0))
		} else {
			copy(copyPacket[8:24], parsed.destination.AsSlice())
			copy(copyPacket[24:40], parsed.source.AsSlice())
			copyPacket[7] = 64

			payload[0] = 129

			sum := networkPseudoSum(parsed.destination, parsed.source, 58, len(payload))

			binary.BigEndian.PutUint16(payload[2:4], networkChecksum(payload, sum))
		}

		gateway.writeFrame(copyPacket)
	})
}
