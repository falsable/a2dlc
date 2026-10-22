package contract

import (
	"os"
	"path/filepath"
	"testing"
)

// The invalid intent examples of the contract are also rejected by Validate: the core does not accept an intent that
// the schema rejects, and the valid ones pass.
func TestIntentValidateFollowsSchema(t *testing.T) {
	for folder, mustPass := range map[string]bool{"valid": true, "invalid": false} {
		examples, _ := filepath.Glob("../../contracts/examples/" + folder + "/intent__*.json")
		if len(examples) < 2 {
			t.Fatalf("%s: %d intent examples", folder, len(examples))
		}
		for _, e := range examples {
			b, _ := os.ReadFile(e)
			var in Intent
			err := DecodeStrictBytes(b, &in)
			if err == nil {
				err = in.Validate()
			}
			if (err == nil) != mustPass {
				t.Errorf("%s: err = %v", filepath.Base(e), err)
			}
		}
	}
}

// What the schema does not express about delivery.rings is required by Validate: strict order and 100 at the end.
func TestIntentValidateDeliveryRings(t *testing.T) {
	b, _ := os.ReadFile("../../contracts/examples/valid/intent__delivery-writes.json")
	var in Intent
	if err := DecodeStrictBytes(b, &in); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rings []int
		ok    bool
	}{{[]int{1, 10, 100}, true}, {[]int{100}, true}, {nil, true}, {[]int{}, false}, {[]int{10, 1, 100}, false}, {[]int{1, 1, 100}, false}, {[]int{1, 50}, false}} {
		in.Delivery.Rings = c.rings
		if err := in.Validate(); (err == nil) != c.ok {
			t.Errorf("rings %v: err = %v", c.rings, err)
		}
	}
	// writes is required: without it we do not know whether CB-06 applies
	in.Delivery.Rings = nil
	in.Delivery.Writes = nil
	if err := in.Validate(); err == nil {
		t.Fatal("delivery without writes accepted")
	}
}
