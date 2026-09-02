package dat

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestParseDomainPreservesRegexCase(t *testing.T) {
	var encoded []byte
	encoded = protowire.AppendTag(encoded, 1, protowire.VarintType)
	encoded = protowire.AppendVarint(encoded, 1)
	encoded = protowire.AppendTag(encoded, 2, protowire.BytesType)
	encoded = protowire.AppendString(encoded, `^host-\S+\.example$`)

	rule, err := parseDomain(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if rule.Value != `^host-\S+\.example$` {
		t.Fatalf("regex = %q, want case preserved", rule.Value)
	}
}
