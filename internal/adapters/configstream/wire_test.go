package configstream

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestResourceFrameRoundTrip(t *testing.T) {
	ref := domain.ResourceRef{Kind: "deployment", ID: "worker/production", Incarnation: "inc", Generation: 42}
	original := Frame{Type: "snapshot", RequestID: 7, Epoch: "epoch", PodUID: "pod", SessionProof: strings.Repeat("a", 64), Refs: []domain.ResourceRef{ref}, Resources: []domain.RuntimeResource{{ResourceRef: ref, Payload: []byte(`{"enabled":true}`), Dependencies: []domain.ResourceRef{{Kind: "version", ID: "v1"}}}}, Fresh: true, Applied: true, ConfirmedMillis: 1700000000000, Nonce: 9}
	raw := Marshal(original)
	// Protobuf's stable field numbers are shared with the native agent.
	if !bytes.HasPrefix(raw, []byte{0x0a, 0x08, 's', 'n', 'a', 'p', 's', 'h', 'o', 't'}) {
		t.Fatalf("unexpected wire envelope: %x", raw)
	}
	raw = protowire.AppendTag(raw, 100, protowire.BytesType)
	raw = protowire.AppendString(raw, "future-field")
	got, err := Unmarshal(raw)
	if err != nil || !reflect.DeepEqual(original, got) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}
func TestResourceFrameRejectsCorruption(t *testing.T) {
	for _, raw := range [][]byte{{0x0a, 0xff}, {0}, {0x08, 0x01}, protowire.AppendBytes(protowire.AppendTag(nil, 5, protowire.BytesType), MarshalRef(domain.ResourceRef{Kind: "version", ID: "v", Generation: -1}))} {
		if _, err := Unmarshal(raw); err == nil {
			t.Fatalf("accepted malformed frame: %x", raw)
		}
	}
}
func FuzzResourceFrame(f *testing.F) {
	f.Add(Marshal(Frame{Type: "hello", PodUID: "pod"}))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		decoded, err := Unmarshal(raw)
		if err == nil {
			if _, err = Unmarshal(Marshal(decoded)); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestResourceFrameBoundsSessionIdentity(t *testing.T) {
	for _, frame := range []Frame{
		{Type: "hello", PodUID: strings.Repeat("p", 129)},
		{Type: "hello", SessionProof: strings.Repeat("s", 129)},
	} {
		if _, err := Unmarshal(Marshal(frame)); err == nil {
			t.Fatal("accepted oversized session identity")
		}
	}
	if _, err := Unmarshal(protowire.AppendVarint(protowire.AppendTag(nil, 15, protowire.VarintType), 1)); err == nil {
		t.Fatal("accepted wrong session proof wire type")
	}
}
