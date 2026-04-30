package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const LocationContentType = "application/vnd.nexus.location+json"

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

func NewLocationPart(location Location) Part {
	raw, _ := json.Marshal(location)
	return Part{ContentType: LocationContentType, Content: string(raw)}
}

func LocationText(location Location) string {
	label := strings.TrimSpace(location.Name)
	if label == "" {
		label = "Location"
	}
	coords := fmt.Sprintf("%.6f, %.6f", location.Latitude, location.Longitude)
	link := fmt.Sprintf("https://maps.google.com/?q=%.6f,%.6f", location.Latitude, location.Longitude)
	if address := strings.TrimSpace(location.Address); address != "" {
		return fmt.Sprintf("%s: %s\n%s\n%s", label, coords, address, link)
	}
	return fmt.Sprintf("%s: %s\n%s", label, coords, link)
}

func ExtractLocations(message Message) []Location {
	locations := make([]Location, 0)
	for _, part := range message.Parts {
		if part.ContentType != LocationContentType || strings.TrimSpace(part.Content) == "" {
			continue
		}
		var location Location
		if err := json.Unmarshal([]byte(part.Content), &location); err != nil {
			continue
		}
		locations = append(locations, location)
	}
	return locations
}
