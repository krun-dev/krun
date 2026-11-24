// Package configstream owns the binary transport, independent of HTTP and SQL.
package configstream

import (
	"errors"

	"github.com/krun-dev/krun/internal/domain"
	"google.golang.org/protobuf/encoding/protowire"
)

type Frame struct {
	Type            string
	RequestID       uint64
	Epoch           string
	PodUID          string
	SessionProof    string
	Refs            []domain.ResourceRef
	Resources       []domain.RuntimeResource
	Error           string
	ConfirmedMillis int64
	Fresh           bool
	Part            uint32
	Parts           uint32
	Chunk           []byte
	Applied         bool
	Nonce           uint64
}

func bytesField(b []byte, n protowire.Number, v []byte) []byte {
	if len(v) == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}
func intField(b []byte, n protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func MarshalRef(r domain.ResourceRef) []byte {
	var b []byte
	b = bytesField(b, 1, []byte(r.Kind))
	b = bytesField(b, 2, []byte(r.ID))
	b = bytesField(b, 3, []byte(r.Incarnation))
	return intField(b, 4, uint64(r.Generation))
}

func MarshalResource(r domain.RuntimeResource) []byte {
	b := bytesField(nil, 1, MarshalRef(r.ResourceRef))
	if r.Deleted {
		b = intField(b, 2, 1)
	}
	b = bytesField(b, 3, r.Payload)
	for _, ref := range r.Dependencies {
		b = bytesField(b, 4, MarshalRef(ref))
	}
	return b
}

func Marshal(f Frame) []byte {
	var b []byte
	b = bytesField(b, 1, []byte(f.Type))
	b = intField(b, 2, f.RequestID)
	b = bytesField(b, 3, []byte(f.Epoch))
	b = bytesField(b, 4, []byte(f.PodUID))
	for _, r := range f.Refs {
		b = bytesField(b, 5, MarshalRef(r))
	}
	for _, r := range f.Resources {
		b = bytesField(b, 6, MarshalResource(r))
	}
	b = bytesField(b, 7, []byte(f.Error))
	b = intField(b, 8, uint64(f.ConfirmedMillis))
	if f.Fresh {
		b = intField(b, 9, 1)
	}
	b = intField(b, 10, uint64(f.Part))
	b = intField(b, 11, uint64(f.Parts))
	b = bytesField(b, 12, f.Chunk)
	b = intField(b, 13, f.Nonce)
	if f.Applied {
		b = intField(b, 14, 1)
	}
	b = bytesField(b, 15, []byte(f.SessionProof))
	return b
}

type field struct {
	number  protowire.Number
	text    []byte
	integer uint64
	typ     protowire.Type
}

func fields(b []byte, consume func(field) error) error {
	for len(b) > 0 {
		n, t, count := protowire.ConsumeTag(b)
		if count < 0 {
			return protowire.ParseError(count)
		}
		b = b[count:]
		f := field{number: n, typ: t}
		switch t {
		case protowire.BytesType:
			f.text, count = protowire.ConsumeBytes(b)
		case protowire.VarintType:
			f.integer, count = protowire.ConsumeVarint(b)
		default:
			count = protowire.ConsumeFieldValue(n, t, b)
		}
		if count < 0 {
			return protowire.ParseError(count)
		}
		b = b[count:]
		if err := consume(f); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalRef(b []byte) (domain.ResourceRef, error) {
	var r domain.ResourceRef
	err := fields(b, func(f field) error {
		if f.number >= 1 && f.number <= 4 {
			expected := protowire.BytesType
			if f.number == 4 {
				expected = protowire.VarintType
			}
			if f.typ != expected {
				return errors.New("invalid resource reference wire type")
			}
		}
		switch f.number {
		case 1:
			r.Kind = string(f.text)
		case 2:
			r.ID = string(f.text)
		case 3:
			r.Incarnation = string(f.text)
		case 4:
			r.Generation = int64(f.integer)
		}
		return nil
	})
	if len(r.Kind) > 32 || len(r.ID) > 512 || len(r.Incarnation) > 128 || r.Generation < 0 {
		return r, errors.New("invalid resource reference")
	}
	return r, err
}

func unmarshalResource(b []byte) (domain.RuntimeResource, error) {
	var r domain.RuntimeResource
	err := fields(b, func(f field) error {
		if f.number >= 1 && f.number <= 4 {
			expected := protowire.BytesType
			if f.number == 2 {
				expected = protowire.VarintType
			}
			if f.typ != expected {
				return errors.New("invalid resource wire type")
			}
		}
		switch f.number {
		case 1:
			ref, err := unmarshalRef(f.text)
			r.ResourceRef = ref
			return err
		case 2:
			r.Deleted = f.integer != 0
		case 3:
			r.Payload = append([]byte(nil), f.text...)
		case 4:
			if len(r.Dependencies) >= 16384 {
				return errors.New("too many dependencies")
			}
			ref, err := unmarshalRef(f.text)
			if err != nil {
				return err
			}
			r.Dependencies = append(r.Dependencies, ref)
		}
		return nil
	})
	return r, err
}

func Unmarshal(b []byte) (Frame, error) {
	var out Frame
	err := fields(b, func(f field) error {
		if f.number >= 1 && f.number <= 15 {
			expected := protowire.BytesType
			switch f.number {
			case 2, 8, 9, 10, 11, 13, 14:
				expected = protowire.VarintType
			}
			if f.typ != expected {
				return errors.New("invalid frame wire type")
			}
			if (f.number == 10 || f.number == 11) && f.integer > 1<<32-1 {
				return errors.New("chunk index overflow")
			}
		}
		switch f.number {
		case 1:
			out.Type = string(f.text)
		case 2:
			out.RequestID = f.integer
		case 3:
			out.Epoch = string(f.text)
		case 4:
			out.PodUID = string(f.text)
		case 5:
			if len(out.Refs) >= 16384 {
				return errors.New("too many references")
			}
			r, err := unmarshalRef(f.text)
			if err != nil {
				return err
			}
			out.Refs = append(out.Refs, r)
		case 6:
			if len(out.Resources) >= 16384 {
				return errors.New("too many resources")
			}
			r, err := unmarshalResource(f.text)
			if err != nil {
				return err
			}
			out.Resources = append(out.Resources, r)
		case 7:
			out.Error = string(f.text)
		case 8:
			out.ConfirmedMillis = int64(f.integer)
		case 9:
			out.Fresh = f.integer != 0
		case 10:
			out.Part = uint32(f.integer)
		case 11:
			out.Parts = uint32(f.integer)
		case 12:
			out.Chunk = append([]byte(nil), f.text...)
		case 13:
			out.Nonce = f.integer
		case 14:
			out.Applied = f.integer != 0
		case 15:
			out.SessionProof = string(f.text)
		}
		return nil
	})
	if len(out.Type) > 32 || len(out.Epoch) > 128 || len(out.PodUID) > 128 || len(out.SessionProof) > 128 {
		return out, errors.New("oversize protocol identity")
	}
	return out, err
}
