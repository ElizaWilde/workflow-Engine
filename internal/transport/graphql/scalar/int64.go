package scalar

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Int64 uses decimal strings on the wire, avoiding JavaScript float precision loss.
type Int64 int64

func (i Int64) MarshalGQL(w io.Writer) {
	_, _ = io.WriteString(w, strconv.Quote(strconv.FormatInt(int64(i), 10)))
}

func (i *Int64) UnmarshalGQL(v any) error {
	var raw string
	switch v := v.(type) {
	case string:
		raw = v
	case json.Number:
		raw = string(v)
	case int:
		raw = strconv.Itoa(v)
	case int64:
		*i = Int64(v)
		return nil
	default:
		return fmt.Errorf("Int64 requires a decimal integer string")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("Int64 is outside the signed 64-bit integer range")
	}
	*i = Int64(n)
	return nil
}
