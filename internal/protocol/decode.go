package protocol

import (
	"encoding/json"
	"fmt"
)

// DecodePayload re-marshals an Envelope's generic Payload (which decodes
// to map[string]interface{} on receipt) into the concrete struct for its
// Type. Callers pass a pointer to the destination struct.
func DecodePayload(env Envelope, dst interface{}) error {
	raw, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("re-marshal payload: %w", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode %s payload: %w", env.Type, err)
	}
	return nil
}
