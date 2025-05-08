package api

import (
	"encoding/binary"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

var TransportGlobalStats = transport.GlobalStats

type TotalResult struct {
	BytesSent           []byte // 8 bytes unsigned-integer bytes representation
	BytesReceived       []byte // 8 bytes unsigned-integer bytes representation
	bytesSentUint64     uint64
	bytesReceivedUint64 uint64
}

func (r TotalResult) String() string {
	return fmt.Sprintf("Total: %s sent, %s received",
		utils.PrettyByteSize(float64(r.bytesSentUint64)), utils.PrettyByteSize(float64(r.bytesReceivedUint64)))
}

func (l *Launcher) Total() (result TotalResult) {
	result.bytesSentUint64, result.bytesReceivedUint64 = TransportGlobalStats.Total()
	binary.LittleEndian.PutUint64(result.BytesSent, result.bytesSentUint64)
	binary.LittleEndian.PutUint64(result.BytesReceived, result.bytesReceivedUint64)
	return result
}

type SpeedResult struct {
	BytesSent     float64
	BytesReceived float64
}

func (r SpeedResult) String() string {
	return fmt.Sprintf("Speed: %s/s sent, %s/s received",
		utils.PrettyByteSize(r.BytesSent), utils.PrettyByteSize(r.BytesReceived))
}

func (l *Launcher) Speed() (result SpeedResult) {
	result.BytesSent, result.BytesReceived = TransportGlobalStats.CalculateSpeed()
	return result
}
