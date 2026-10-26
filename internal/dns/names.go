package dns

import (
	"fmt"
	"strings"
)

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 253 {
		return fmt.Errorf("name %q is longer than 253 characters", name)
	}
	labels := strings.Split(name, ".")
	for i, l := range labels {
		if l == "" {
			return fmt.Errorf("name %q has an empty label", name)
		}
		if len(l) > maxLabelLen {
			return fmt.Errorf("label %q is longer than 63 characters", l)
		}
		if l == "*" {
			if i != 0 {
				return fmt.Errorf("wildcard is only allowed as the leftmost label in %q", name)
			}
			continue
		}
		for j := 0; j < len(l); j++ {
			c := l[j]
			ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
			if !ok {
				return fmt.Errorf("label %q contains invalid character %q", l, c)
			}
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label %q must not start or end with a hyphen", l)
		}
	}
	return nil
}
