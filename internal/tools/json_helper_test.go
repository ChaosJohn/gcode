package tools

import "encoding/json"

func jsonImpl(raw []byte, target any) error { return json.Unmarshal(raw, target) }
