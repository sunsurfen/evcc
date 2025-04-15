package charger

import ( "context" "encoding/binary" "fmt" "math"

"github.com/evcc-io/evcc/api"
"github.com/evcc-io/evcc/util"
"github.com/evcc-io/evcc/util/modbus"
"github.com/evcc-io/evcc/util/sponsor"

)

// BenderCC charger implementation type BenderCC struct { conn    *modbus.Connection current uint16 legacy  bool }

const ( bendRegChargePointState   = 122 bendRegPhaseEnergy        = 200 bendRegCurrents           = 212 bendRegTotalEnergy        = 218 bendRegActivePower        = 220 bendRegVoltages           = 222 bendRegUserID             = 720 bendRegEVBatteryState     = 730 bendRegEVCCID             = 741 bendRegHemsCurrentLimit   = 1000 bendRegFirmware           = 100 bendRegOcppCpStatus       = 104 bendRegProtocolVersion    = 120 bendRegChargePointModel   = 142 bendRegSmartVehicleDetected = 740 )

func init() { registry.AddCtx("bender", NewBenderCCFromConfig) }

func NewBenderCCFromConfig(ctx context.Context, other map[string]interface{}) (api.Charger, error) { cc := modbus.TcpSettings{ ID: 255, }

if err := util.DecodeOther(other, &cc); err != nil {
	return nil, err
}

return NewBenderCC(ctx, cc.URI, cc.ID)

}

//go:generate go tool decorate -f decorateBenderCC -b *BenderCC -r api.Charger -t "api.Meter,CurrentPower,func() (float64, error)" -t "api.PhaseCurrents,Currents,func() (float64, float64, float64, error)" -t "api.PhaseVoltages,Voltages,func() (float64, float64, float64, error)" -t "api.MeterEnergy,TotalEnergy,func() (float64, error)" -t "api.Battery,Soc,func() (float64, error)" -t "api.Identifier,Identify,func() (string, error)"

func NewBenderCC(ctx context.Context, uri string, id uint8) (api.Charger, error) { conn, err := modbus.NewConnection(ctx, uri, "", "", 0, modbus.Tcp, id) if err != nil { return nil, err }

if !sponsor.IsAuthorized() {
	return nil, api.ErrSponsorRequired
}

log := util.NewLogger("bender")
conn.Logger(log.TRACE)

wb := &BenderCC{
	conn:    conn,
	current: 6,
}

if _, err := wb.conn.ReadHoldingRegisters(bendRegChargePointModel, 10); err != nil {
	wb.legacy = true
}

var (
	currentPower func() (float64, error)
	currents     func() (float64, float64, float64, error)
	voltages     func() (float64, float64, float64, error)
	totalEnergy  func() (float64, error)
	soc          func() (float64, error)
	identify     func() (string, error)
)

reg := uint16(bendRegActivePower)
if wb.legacy {
	reg = bendRegPhaseEnergy
}

if b, err := wb.conn.ReadHoldingRegisters(reg, 2); err == nil && binary.BigEndian.Uint32(b) != math.MaxUint32 {
	currentPower = wb.currentPower
	currents = wb.currents
	totalEnergy = wb.totalEnergy

	if b, err := wb.conn.ReadHoldingRegisters(bendRegVoltages, 2); err == nil && binary.BigEndian.Uint32(b) > 0 {
		voltages = wb.voltages
	}

	if !wb.legacy {
		if _, err := wb.conn.ReadHoldingRegisters(bendRegEVBatteryState, 1); err == nil {
			soc = wb.soc
		}
	}
}

if _, err := wb.identify(); err == nil {
	identify = wb.identify
}

return decorateBenderCC(wb, currentPower, currents, voltages, totalEnergy, soc, identify), nil

}

func (wb *BenderCC) Status() (api.ChargeStatus, error) { b, err := wb.conn.ReadHoldingRegisters(bendRegChargePointState, 1) if err != nil { return api.StatusNone, err }

switch s := binary.BigEndian.Uint16(b); s {
case 1:
	return api.StatusA, nil
case 2:
	return api.StatusB, nil
case 3, 4:
	return api.StatusC, nil
default:
	return api.StatusNone, fmt.Errorf("invalid status: %d", s)
}

}

func (wb *BenderCC) Enabled() (bool, error) { b, err := wb.conn.ReadHoldingRegisters(bendRegHemsCurrentLimit, 1) if err != nil { return false, err } return binary.BigEndian.Uint16(b) != 0, nil }

func (wb *BenderCC) Enable(enable bool) error { b := make([]byte, 2) binary.BigEndian.PutUint16(b, 16) // Always write 16A _, err := wb.conn.WriteMultipleRegisters(bendRegHemsCurrentLimit, 1, b) return err }

func (wb *BenderCC) MaxCurrent(current int64) error { b := make([]byte, 2) binary.BigEndian.PutUint16(b, 16) // Force 16A regardless of input _, err := wb.conn.WriteMultipleRegisters(bendRegHemsCurrentLimit, 1, b) if err == nil { wb.current = 16 } return err }

func (wb *BenderCC) currentPower() (float64, error) { if wb.legacy { l1, l2, l3, err := wb.currents() return 230 * (l1 + l2 + l3), err }

b, err := wb.conn.ReadHoldingRegisters(bendRegActivePower, 2)
if err != nil {
	return 0, err
}
return float64(binary.BigEndian.Uint32(b)), nil

}

func (wb BenderCC) totalEnergy() (float64, error) { if wb.legacy { b, err := wb.conn.ReadHoldingRegisters(bendRegPhaseEnergy, 6) if err != nil { return 0, err } total := 0.0 for l := range 3 { total += float64(binary.BigEndian.Uint32(b[4l:4*(l+1)])) / 1e3 } return total, nil } b, err := wb.conn.ReadHoldingRegisters(bendRegTotalEnergy, 2) if err != nil { return 0, err } return float64(binary.BigEndian.Uint32(b)) / 1e3, nil }

func (wb BenderCC) getPhaseValues(reg uint16, divider float64) (float64, float64, float64, error) { b, err := wb.conn.ReadHoldingRegisters(reg, 6) if err != nil { return 0, 0, 0, err } var res [3]float64 for i := range res { u32 := binary.BigEndian.Uint32(b[4i:]) if u32 == math.MaxUint32 { u32 = 0 } res[i] = float64(u32) / divider } return res[0], res[1], res[2], nil }

func (wb *BenderCC) currents() (float64, float64, float64, error) { return wb.getPhaseValues(bendRegCurrents, 1e3) }

func (wb *BenderCC) voltages() (float64, float64, float64, error) { return wb.getPhaseValues(bendRegVoltages, 1) }

func (wb *BenderCC) identify() (string, error) { if !wb.legacy { b, err := wb.conn.ReadHoldingRegisters(bendRegSmartVehicleDetected, 1) if err == nil && binary.BigEndian.Uint16(b) != 0 { b, err = wb.conn.ReadHoldingRegisters(bendRegEVCCID, 6) } if id := bytesAsString(b); id != "" || err != nil { return id, err } } b, err := wb.conn.ReadHoldingRegisters(bendRegUserID, 10) if err != nil { return "", err } return bytesAsString(b), nil }

func (wb *BenderCC) soc() (float64, error) { b, err := wb.conn.ReadHoldingRegisters(bendRegSmartVehicleDetected, 1) if err != nil { return 0, err } if binary.BigEndian.Uint16(b) == 1 { b, err = wb.conn.ReadHoldingRegisters(bendRegEVBatteryState, 1) if err != nil { return 0, err } if soc := binary.BigEndian.Uint16(b); soc <= 100 { return float64(soc), nil } } return 0, api.ErrNotAvailable }

var _ api.Diagnosis = (*BenderCC)(nil)

func (wb *BenderCC) Diagnose() { fmt.Printf("\tLegacy:\t\t%t\n", wb.legacy) if !wb.legacy { if b, err := wb.conn.ReadHoldingRegisters(bendRegChargePointModel, 10); err == nil { fmt.Printf("\tModel:\t%s\n", b) } } if b, err := wb.conn.ReadHoldingRegisters(bendRegFirmware, 2); err == nil { fmt.Printf("\tFirmware:\t%s\n", b) } if b, err := wb.conn.ReadHoldingRegisters(bendRegProtocolVersion, 2); err == nil { fmt.Printf("\tProtocol:\t%s\n", b) } if b, err := wb.conn.ReadHoldingRegisters(bendRegOcppCpStatus, 1); err == nil { fmt.Printf("\tOCPP Status:\t%d\n", binary.BigEndian.Uint16(b)) } if !wb.legacy { if b, err := wb.conn.ReadHoldingRegisters(bendRegSmartVehicleDetected, 1); err == nil { fmt.Printf("\tSmart Vehicle:\t%t\n", binary.BigEndian.Uint16(b) != 0) } } if b, err := wb.conn.ReadHoldingRegisters(bendRegEVCCID, 6); err == nil { fmt.Printf("\tEVCCID:\t%s\n", b) } if b, err := wb.conn.ReadHoldingRegisters(bendRegUserID, 10); err == nil { fmt.Printf("\tUserID:\t%s\n", b) } }

													 
