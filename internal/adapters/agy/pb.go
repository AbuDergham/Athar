package agy

import (
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// pbField is one decoded protobuf field: a varint or a length-delimited value.
// Other wire types are skipped over and keep zero values.
type pbField struct {
	num    protowire.Number
	typ    protowire.Type
	varint uint64
	bytes  []byte
}

// pbMessage is the top level of a decoded protobuf message, fields in wire order.
type pbMessage []pbField

// parseMessage decodes one level of a protobuf message. ok is false when b is
// not a well-formed message (truncated, bad tag, bad wire type).
func parseMessage(b []byte) (pbMessage, bool) {
	var out pbMessage
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, false
		}
		b = b[n:]
		f := pbField{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			f.varint, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			f.bytes, n = protowire.ConsumeBytes(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return nil, false
		}
		b = b[n:]
		out = append(out, f)
	}
	return out, true
}

// bytesField returns the last length-delimited value of field num (for a
// singular field the last occurrence wins, as in protobuf).
func (m pbMessage) bytesField(num protowire.Number) ([]byte, bool) {
	var out []byte
	found := false
	for _, f := range m {
		if f.num == num && f.typ == protowire.BytesType {
			out, found = f.bytes, true
		}
	}
	return out, found
}

// all returns every length-delimited value of the repeated field num, in order.
func (m pbMessage) all(num protowire.Number) [][]byte {
	var out [][]byte
	for _, f := range m {
		if f.num == num && f.typ == protowire.BytesType {
			out = append(out, f.bytes)
		}
	}
	return out
}

// str returns the string field num ("" when absent), with invalid UTF-8 replaced.
func (m pbMessage) str(num protowire.Number) string {
	b, _ := m.bytesField(num)
	return strings.ToValidUTF8(string(b), "�")
}

// sub decodes the embedded message field num.
func (m pbMessage) sub(num protowire.Number) (pbMessage, bool) {
	b, ok := m.bytesField(num)
	if !ok {
		return nil, false
	}
	return parseMessage(b)
}

// timeField decodes the google.protobuf.Timestamp-shaped field num.
func (m pbMessage) timeField(num protowire.Number) (time.Time, bool) {
	b, ok := m.bytesField(num)
	if !ok {
		return time.Time{}, false
	}
	return pbTimestamp(b)
}

// pbTimestamp decodes a Timestamp message {1: seconds, 2: nanos} to UTC.
// ok is false for malformed input or a non-positive seconds value.
func pbTimestamp(b []byte) (time.Time, bool) {
	m, ok := parseMessage(b)
	if !ok {
		return time.Time{}, false
	}
	var secs, nanos int64
	for _, f := range m {
		if f.typ != protowire.VarintType {
			continue
		}
		switch f.num {
		case 1:
			secs = int64(f.varint)
		case 2:
			nanos = int64(f.varint)
		}
	}
	if secs <= 0 || nanos < 0 || nanos >= int64(time.Second) {
		return time.Time{}, false
	}
	return time.Unix(secs, nanos).UTC(), true
}
