package pmetrics

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// reservedPrefix starts the label names a metrics store keeps for itself.
const reservedPrefix = "__"

// ParseLabels reads labels from a setting written name=value,name=value, as LEONTP_METRICS_LABELS=hostname=ntp,site=home is; spaces around a name or
// a value are dropped, and nothing at all is no labels. A value cannot hold a comma.
func ParseLabels(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}

	labels := map[string]string{}
	for pair := range strings.SplitSeq(s, ",") {
		name, value, found := strings.Cut(pair, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)

		if !found {
			return nil, fmt.Errorf("the label %q is not written name=value", strings.TrimSpace(pair))
		}

		if _, twice := labels[name]; twice {
			return nil, fmt.Errorf("the label %q is given twice", name)
		}

		labels[name] = value
	}

	if err := ValidateLabels(labels); err != nil {
		return nil, err
	}

	return labels, nil
}

// ValidateLabels says what is wrong with the first label, by name, that a metrics store would not take: a name that is not letters, digits and
// underscores, one that starts with a digit or with the two underscores a store keeps for itself, or a label with no value.
func ValidateLabels(labels map[string]string) error {
	// in order, so the same mistake is always the one reported
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		if !namePattern.MatchString(name) || strings.HasPrefix(name, reservedPrefix) {
			return fmt.Errorf("%q cannot be a label's name: letters, digits and underscores, not starting with a digit or with two underscores", name)
		}

		if labels[name] == "" {
			return fmt.Errorf("the label %q has no value", name)
		}
	}

	return nil
}
