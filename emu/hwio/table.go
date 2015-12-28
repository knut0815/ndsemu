package hwio

import (
	"fmt"
	"ndsemu/emu"

	log "gopkg.in/Sirupsen/logrus.v0"
)

type BankIO8 interface {
	Read8(addr uint32) uint8
	Write8(addr uint32, val uint8)
}

type BankIO16 interface {
	Read16(addr uint32) uint16
	Write16(addr uint32, val uint16)
}

type BankIO32 interface {
	Read32(addr uint32) uint32
	Write32(addr uint32, val uint32)
}

type BankIO interface {
	BankIO8
	BankIO16
	BankIO32
}

type Table struct {
	Name string

	table8  [0x1000]BankIO8
	table16 [0x1000 / 2]BankIO16
	table32 [0x1000 / 4]BankIO32
}

type io32to16 Table

func (t *io32to16) Read32(addr uint32) uint32 {
	idx := (addr & 0xFFF) >> 1
	val1 := (*Table)(t).table16[idx+0].Read16(addr + 0)
	val2 := (*Table)(t).table16[idx+1].Read16(addr + 2)
	return uint32(val1) | uint32(val2)<<16
}

func (t *io32to16) Write32(addr uint32, val uint32) {
	idx := (addr & 0xFFF) >> 1
	(*Table)(t).table16[idx+0].Write16(addr, uint16(val&0xFFFF))
	(*Table)(t).table16[idx+1].Write16(addr+2, uint16(val>>16))
}

func (t *Table) Reset() {
	for i := range t.table8 {
		t.table8[i] = nil
	}
	for i := range t.table16 {
		t.table16[i] = nil
	}
	for i := range t.table32 {
		t.table32[i] = nil
	}
}

// Map a register bank (that is, a structure containing mulitple IoReg* fields).
// For this function to work, registers must have a struct tag "hwio", containing
// the following fields:
//
//      offset=0x12     Byte-offset within the register bank at which this
//                      register is mapped. There is no default value: if this
//                      option is missing, the register is assumed not to be
//                      part of the bank, and is ignored by this call.
//
//      bank=NN         Ordinal bank number (if not specified, default to zero).
//                      This option allows for a structure to expose multiple
//                      banks, as regs can be grouped by bank by specified the
//                      bank number.
//
func (t *Table) MapBank(addr uint32, bank interface{}, bankNum int) {
	regs, err := bankGetRegs(bank, bankNum)
	if err != nil {
		panic(err)
	}

	for _, reg := range regs {
		switch r := reg.regPtr.(type) {
		case *Reg64:
			t.MapReg64(addr+reg.offset, r)
		case *Reg32:
			t.MapReg32(addr+reg.offset, r)
		case *Reg16:
			t.MapReg16(addr+reg.offset, r)
		default:
			panic(fmt.Errorf("invalid reg type: %T", r))
		}
	}
}

func (t *Table) MapReg64(addr uint32, io *Reg64) {
	addr &= 0xFFF
	if addr&7 != 0 {
		panic("unaligned mapping")
	}
	for i := 0; i < 8; i++ {
		if t.table8[addr] != nil {
			panic("address already mapped")
		}
		t.table8[addr] = io
		t.table16[addr>>1] = io
		t.table32[addr>>2] = io
		addr++
	}
}

func (t *Table) MapReg32(addr uint32, io *Reg32) {
	addr &= 0xFFF
	if addr&3 != 0 {
		panic("unaligned mapping")
	}
	for i := 0; i < 4; i++ {
		if t.table8[addr] != nil {
			panic("address already mapped")
		}
		t.table8[addr] = io
		t.table16[addr>>1] = io
		t.table32[addr>>2] = io
		addr++
	}
}

func (t *Table) MapReg16(addr uint32, io *Reg16) {
	addr &= 0xFFF
	if addr&1 != 0 {
		panic("unaligned mapping")
	}
	for i := 0; i < 2; i++ {
		if t.table8[addr] != nil {
			panic("address already mapped")
		}
		t.table8[addr] = io
		t.table16[addr>>1] = io
		t.table32[addr>>2] = (*io32to16)(t)
		addr++
	}
}

func (t *Table) Read8(addr uint32) uint8 {
	io := t.table8[addr&0xFFF]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Read8")
		return 0
	}
	return io.Read8(addr)
}

func (t *Table) Write8(addr uint32, val uint8) {
	io := t.table8[addr&0xFFF]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"val":  emu.Hex8(val),
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Write8")
		return
	}
	io.Write8(addr, val)
}

func (t *Table) Read16(addr uint32) uint16 {
	io := t.table16[(addr&0xFFF)>>1]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Read16")
		return 0
	}
	return io.Read16(addr)
}

func (t *Table) Write16(addr uint32, val uint16) {
	io := t.table16[(addr&0xFFF)>>1]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"val":  emu.Hex16(val),
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Write16")
		return
	}
	io.Write16(addr, val)
}

func (t *Table) Read32(addr uint32) uint32 {
	io := t.table32[(addr&0xFFF)>>2]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Read32")
		return 0
	}
	return io.Read32(addr)
}

func (t *Table) Write32(addr uint32, val uint32) {
	io := t.table32[(addr&0xFFF)>>2]
	if io == nil {
		log.WithFields(log.Fields{
			"name": t.Name,
			"val":  emu.Hex32(val),
			"addr": emu.Hex32(addr),
		}).Error("[IO] unmapped Write32")
		return
	}
	io.Write32(addr, val)
}