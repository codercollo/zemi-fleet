package main

import (
	"strings"
	"testing"
)

func TestGenerateSQLIsDeterministicAndScoped(t *testing.T) {
	config := options{riders: 2, orders: 3, seed: 7}
	first := generateSQL(config)
	second := generateSQL(config)

	if first != second {
		t.Fatal("load SQL is not deterministic")
	}
	for _, fragment := range []string{"load_rider_000001", "load_ord_00000001", "DELETE FROM orders WHERE id LIKE 'load_ord_%'", "COMMIT;"} {
		if !strings.Contains(first, fragment) {
			t.Fatalf("generated SQL does not contain %q", fragment)
		}
	}
}

func TestOrderGeometryProducesUsableTrips(t *testing.T) {
	_, _, pickupLat, pickupLng, deliveryLat, deliveryLng := orderLocations(1, 20261019)
	if distance := haversineMetres(pickupLat, pickupLng, deliveryLat, deliveryLng); distance < 50 {
		t.Fatalf("generated trip is too short: %d metres", distance)
	}
	bearing := bearingDegrees(pickupLat, pickupLng, deliveryLat, deliveryLng)
	if bearing < 0 || bearing >= 360 {
		t.Fatalf("generated bearing is out of range: %f", bearing)
	}
}
