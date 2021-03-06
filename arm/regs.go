package arm

import (
	"fmt"

	log "gopkg.in/Sirupsen/logrus.v0"
)

type reg uint32

func boolToReg(f bool) reg {
	// Use a form that the compiler can optimize into a SETxx (https://github.com/golang/go/issues/6011)
	var i reg
	if f {
		i = 1
	}
	return i
}

func (r reg) Bit(n uint) bool { return ((uint32(r) >> n) & 1) != 0 }

func (r *reg) BitSet(n uint)   { *r |= 1 << n }
func (r *reg) BitClear(n uint) { *r &= ^(1 << n) }
func (r *reg) BitChange(n uint, f bool) {
	i := boolToReg(f)
	*r = ((*r) &^ (1 << n)) | i<<n
}

func (r *reg) SetWithMask(val uint32, mask uint32) {
	*r = reg(((uint32)(*r) &^ mask) | (val & mask))
}

func (r reg) String() string {
	return fmt.Sprintf("%08X", uint32(r))
}

type regCpsr struct {
	r reg
}

func (r regCpsr) CB() uint32 { return (uint32(r.r) >> 29) & 1 }

// We don't use Bit() here because of the Go compiler is too sucky at
// optimizations and we don't want to create overhead on the hot paths
func (r regCpsr) N() bool { return r.r&(1<<31) != 0 }
func (r regCpsr) Z() bool { return r.r&(1<<30) != 0 }
func (r regCpsr) C() bool { return r.r&(1<<29) != 0 }
func (r regCpsr) V() bool { return r.r&(1<<28) != 0 }
func (r regCpsr) Q() bool { return r.r&(1<<27) != 0 }
func (r regCpsr) I() bool { return r.r&(1<<7) != 0 }
func (r regCpsr) F() bool { return r.r&(1<<6) != 0 }
func (r regCpsr) T() bool { return r.r&(1<<5) != 0 }

func (r *regCpsr) SetNZ(val uint32) {
	r.r &= 0x3FFFFFFF
	r.r |= reg(val & 0x80000000)
	i := boolToReg(val == 0)
	r.r |= i << 30
}

func (r *regCpsr) SetNZ64(val uint64) {
	r.r &= 0x3FFFFFFF
	r.r |= reg((val >> 32) & 0x80000000)
	i := boolToReg(val == 0)
	r.r |= i << 30
}

func (r *regCpsr) SetC(val bool) {
	r.r.BitChange(29, val)
}

func (r *regCpsr) SetVAdd(s1, s2, res uint32) {
	v := ^(s1 ^ s2) & (s1 ^ res) & 0x80000000
	r.r &^= 0x10000000
	r.r |= reg(v >> 3)
}

func (r *regCpsr) SetVSub(s1, s2, res uint32) {
	v := ((s1 ^ s2) & (s1 ^ res) & 0x80000000)
	r.r &^= 0x10000000
	r.r |= reg(v >> 3)
}

func (r *regCpsr) SetI(val bool) {
	r.r.BitChange(7, val)
}

func (r *regCpsr) SetF(val bool) {
	r.r.BitChange(6, val)
}

func (r *regCpsr) SetT(val bool) {
	r.r.BitChange(5, val)
}

func (r *regCpsr) GetMode() CpuMode {
	return CpuMode(r.r & 0x1F)
}

func (r *regCpsr) SetMode(mode CpuMode, cpu *Cpu) {
	r.SetWithMask(uint32(mode), 0x1F, cpu)
}

func (r *regCpsr) Set(val uint32, cpu *Cpu) {
	r.SetWithMask(val, 0xFFFFFFFF, cpu)
}

func (r *regCpsr) Uint32() uint32 {
	return uint32(r.r)
}

func (r *regCpsr) SetWithMask(val uint32, mask uint32, cpu *Cpu) {
	oldmode := CpuMode(r.r & 0x1F)
	r.r = (r.r &^ reg(mask)) | reg(val&mask)
	mode := CpuMode(r.r & 0x1F)

	// If the I/F bits are potentially changed, we must force
	// exit the tight loop, to check if the new bits will cause
	// an interrupt right away.
	if mask&0xC0 != 0 {
		cpu.tightExit = true
	}

	if mode == oldmode {
		return
	}

	// log.WithFields(log.Fields{
	// 	"mode": mode,
	// 	"old":  oldmode,
	// 	"pc":   cpu.GetPC(),
	// }).Info("changing CPSR mode")

	switch oldmode {
	case CpuModeUser, CpuModeSystem:
		copy(cpu.UsrBank[:], cpu.Regs[13:15])
	case CpuModeFiq:
		copy(cpu.FiqBank2[:], cpu.Regs[8:13])
		copy(cpu.Regs[8:13], cpu.UsrBank2[:])
		copy(cpu.FiqBank[:], cpu.Regs[13:15])
	case CpuModeIrq:
		copy(cpu.IrqBank[:], cpu.Regs[13:15])
	case CpuModeSupervisor:
		copy(cpu.SvcBank[:], cpu.Regs[13:15])
	case CpuModeAbort:
		copy(cpu.AbtBank[:], cpu.Regs[13:15])
	case CpuModeUndefined:
		copy(cpu.UndBank[:], cpu.Regs[13:15])
	default:
		log.Fatalf("unknown CPU oldmode: %v", oldmode)
	}

	switch mode {
	case CpuModeUser, CpuModeSystem:
		copy(cpu.Regs[13:15], cpu.UsrBank[:])
	case CpuModeFiq:
		copy(cpu.UsrBank2[:], cpu.Regs[8:13])
		copy(cpu.Regs[8:13], cpu.FiqBank2[:])
		copy(cpu.Regs[13:15], cpu.FiqBank[:])
	case CpuModeIrq:
		copy(cpu.Regs[13:15], cpu.IrqBank[:])
	case CpuModeSupervisor:
		copy(cpu.Regs[13:15], cpu.SvcBank[:])
	case CpuModeAbort:
		copy(cpu.Regs[13:15], cpu.AbtBank[:])
	case CpuModeUndefined:
		copy(cpu.Regs[13:15], cpu.UndBank[:])
	default:
		log.Fatalf("unknown CPU newmode: %v", mode)
	}
}

func (r regCpsr) String() string {
	return r.r.String()
}

type CpuMode int

const (
	CpuModeUser       CpuMode = 0x10
	CpuModeFiq        CpuMode = 0x11
	CpuModeIrq        CpuMode = 0x12
	CpuModeSupervisor CpuMode = 0x13
	CpuModeAbort      CpuMode = 0x17
	CpuModeUndefined  CpuMode = 0x18
	CpuModeSystem     CpuMode = 0x1F
)

func (m CpuMode) String() string {
	switch m {
	case CpuModeUser:
		return "CpuModeUser"
	case CpuModeFiq:
		return "CpuModeFiq"
	case CpuModeIrq:
		return "CpuModeIrq"
	case CpuModeSupervisor:
		return "CpuModeSupervisor"
	case CpuModeAbort:
		return "CpuModeAbort"
	case CpuModeUndefined:
		return "CpuModeUndefined"
	case CpuModeSystem:
		return "CpuModeSystem"
	default:
		return fmt.Sprintf("CpuMode(%02x)", int(m))
	}
}
