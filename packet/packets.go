/*
 * go-mysqlstack
 * xelabs.org
 *
 * Copyright (c) XeLabs
 * GPL License
 *
 */

package packet

import (
	"io"

	"github.com/XeLabs/go-mysqlstack/common"
	"github.com/XeLabs/go-mysqlstack/proto"
	"github.com/pkg/errors"
)

const (
	PACKET_MAX_SIZE = (1<<24 - 1) // (16MB - 1）
)

type Packet struct {
	SequenceID byte
	Payload    []byte
}

type Packets struct {
	seq    uint8
	stream *Stream
}

func NewPackets(rw io.ReadWriter) *Packets {
	return &Packets{
		stream: NewStream(rw, PACKET_MAX_SIZE),
	}
}

// Read reads packet from stream
func (p *Packets) Next() (v []byte, e error) {
	pkt, err := p.stream.Read()
	if err != nil {
		e = err
		return
	}

	if pkt.SequenceID != p.seq {
		e = errors.Errorf("pkt.read.seq[%v]!=pkt.actual.seq[%v]", pkt.SequenceID, p.seq)
		return
	}
	p.seq++

	return pkt.Payload, nil
}

// Write writes the packet to stream
// It packs as:
// [header]
// [payload]
func (p *Packets) Write(payload []byte) error {
	payLen := len(payload)
	pkt := common.NewBuffer(128)

	// body length(24bits)
	pkt.WriteU24(uint32(payLen))

	// SequenceID
	pkt.WriteU8(p.seq)

	// body
	pkt.WriteBytes(payload)
	if err := p.stream.Write(pkt.Datas()); err != nil {
		return err
	}
	p.seq++

	return nil
}

// WriteCommand writes a command packet to stream
func (p *Packets) WriteCommand(command byte, payload []byte) error {
	// reset packet sequence
	p.seq = 0
	pkt := common.NewBuffer(128)

	// body length(24bits):
	// command length + payload length
	payLen := len(payload)
	pkt.WriteU24(uint32(1 + payLen))

	// SequenceID
	pkt.WriteU8(p.seq)

	// command
	pkt.WriteU8(command)

	// body
	pkt.WriteBytes(payload)
	if err := p.stream.Write(pkt.Datas()); err != nil {
		return err
	}
	p.seq++

	return nil
}

// Append appends packets to buffer but not write to stream
// NOTICE: SequenceID++
func (p *Packets) Append(buff *common.Buffer, rawdata []byte) error {
	pkt := common.NewBuffer(128)

	// body length(24bits):
	// payload length
	pkt.WriteU24(uint32(len(rawdata)))

	// SequenceID
	pkt.WriteU8(p.seq)

	// body
	pkt.WriteBytes(rawdata)
	if err := p.stream.Append(buff, pkt.Datas()); err != nil {
		return err
	}
	p.seq++

	return nil
}

// AppendEOF appends EOF packet to buff
func (p *Packets) AppendEOF(buff *common.Buffer) error {
	return p.Append(buff, []byte{proto.EOF_PACKET})
}

// Flush writes all append-packets to stream
func (p *Packets) Flush(packets []byte) error {
	return p.stream.Flush(packets)
}

// ResetSeq reset sequence to zero
func (p *Packets) ResetSeq() {
	p.seq = 0
}

func (p *Packets) ParseERR(data []byte, capabilityFlags uint32) (e *proto.ERR, err error) {
	e = proto.NewERR()
	err = e.UnPack(data, capabilityFlags)

	return
}

func (p *Packets) ParseOK(data []byte, capabilityFlags uint32) (ok *proto.OK, err error) {
	ok = proto.NewOK()
	err = ok.UnPack(data, capabilityFlags)

	return
}

// ReadColumns parses columns info
// http://dev.mysql.com/doc/internals/en/com-query-response.html#packet-ProtocolText::Resultset
func (p *Packets) ReadColumns(capabilityFlags uint32) (
	columns []*proto.Column,
	ok *proto.OK,
	err error) {
	var count uint64
	var payload []byte
	var pkt *proto.ERR

	if payload, err = p.Next(); err != nil {
		return
	}

	ok = proto.NewOK()
	switch payload[0] {
	case proto.OK_PACKET:
		// maybe we are OK response, such as Exec response
		if ok, err = p.ParseOK(payload, capabilityFlags); err != nil {
			return
		}
		return

	case proto.ERR_PACKET:
		if pkt, err = p.ParseERR(payload, capabilityFlags); err != nil {
			return
		}
		err = errors.New(pkt.ErrorMessage)
		return
	}

	// column count
	if count, err = proto.ColumnCount(payload); err != nil {
		return
	}

	// column info
	columns = make([]*proto.Column, 0, count)
	for i := 0; i < int(count); i++ {
		if payload, err = p.Next(); err != nil {
			return
		}

		col := &proto.Column{}
		if err = col.UnPack(payload); err != nil {
			return
		}
		columns = append(columns, col)
	}

	// EOF packet
	if payload, err = p.Next(); err != nil {
		return
	} else {
		if payload[0] != proto.EOF_PACKET {
			err = errors.Errorf("read.columns.EOF.error.columns[%+v]", columns)
			return
		}
	}
	return
}

// WriteColumns writes columns packet to stream
func (p *Packets) WriteColumns(columns []*proto.Column) (err error) {
	batch := common.NewBuffer(128)

	// column count
	count := len(columns)
	buf := common.NewBuffer(64)
	buf.WriteLenEncode(uint64(count))
	if err = p.Append(batch, buf.Datas()); err != nil {
		return
	}

	// columns info
	for i := 0; i < count; i++ {
		buf := common.NewBuffer(64)
		buf.WriteBytes(columns[i].Pack())
		if err = p.Append(batch, buf.Datas()); err != nil {
			return
		}
	}
	// EOF
	if err = p.AppendEOF(batch); err != nil {
		return err
	}

	// write to stream
	if err = p.Flush(batch.Datas()); err != nil {
		return err
	}

	return
}