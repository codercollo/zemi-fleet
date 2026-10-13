package api

import (
	"context"
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

func TestKESToCents(t *testing.T) {
	tests := []struct {
		name      string
		kes       float64
		wantCents int64
		wantOK    bool
	}{
		{"zero", 0, 0, true},
		{"a cent", 0.01, 1, true},
		{"0.1 + 0.2 is 0.30000000000000004 in float64, and is 30 cents", 0.1 + 0.2, 30, true},
		{"19.99 is 1998.9999999999998 cents in float64; rounding gives 1999, truncating would lose a cent", 19.99, 1999, true},
		{"4500.50", 4500.50, 450050, true},
		{"1.15 (1.15 * 100 = 114.99999999999999)", 1.15, 115, true},
		{"0.07 (0.07 * 100 = 7.000000000000001)", 0.07, 7, true},
		{"the configured maximum, 10,000,000 KES", 10_000_000, 1_000_000_000, true},
		{"the maximum plus a cent converts fine (the bound is enforced by checkAmount, not here)", 10_000_000.01, 1_000_000_001, true},
		{"a billion KES", 1e9, 100_000_000_000, true},
		{"negative amounts convert (callers reject them)", -12.34, -1234, true},

		{"10.005 has a third decimal", 10.005, 0, false},
		{"0.001 has a third decimal", 0.001, 0, false},
		{"1.999", 1.999, 0, false},
		{"0.1 + 0.2 + 0.005", 0.1 + 0.2 + 0.005, 0, false},
		{"NaN", math.NaN(), 0, false},
		{"+Inf", math.Inf(1), 0, false},
		{"-Inf", math.Inf(-1), 0, false},
		{"1e300 would overflow", 1e300, 0, false},
		{"just over the conversion limit", 1.1e13, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cents, ok := kesToCents(tc.kes)

			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantCents, cents)
			}
		})
	}
}

// Every amount a client can type with two decimals, from 0.00 up to 20,000.00, round-trips to the exact cent.
func TestKESToCents_EveryCentRoundTrips(t *testing.T) {
	for cents := int64(0); cents <= 2_000_000; cents++ {
		got, ok := kesToCents(float64(cents) / 100)
		if !ok || got != cents {
			t.Fatalf("%d cents -> %v KES -> %d (ok=%v)", cents, float64(cents)/100, got, ok)
		}
	}
	for _, cents := range []int64{999_999_999, 1_000_000_000, 123_456_789_01} {
		got, ok := kesToCents(float64(cents) / 100)
		require.True(t, ok)
		assert.Equal(t, cents, got)
	}
}

func TestCentsToKESIsOutputOnly(t *testing.T) {
	assert.Equal(t, 19.99, centsToKES(1999))
	assert.Equal(t, 0.0, centsToKES(0))
	assert.Equal(t, 10_000_000.0, centsToKES(1_000_000_000))
}

func orderWith(value, fee string) string {
	return `{"vendor_id":"ven_001","category":"electronics","delivery_location":{"lat":-1.2905,"lng":36.7870},"value_kes":` + value + `,"delivery_fee_kes":` + fee + `}`
}

func TestCreateOrder_AmountBounds(t *testing.T) {
	t.Run("the configured maximum is accepted for both amounts", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(fakeVendor(), nil)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) {
				assert.EqualValues(t, 1_000_000_000, p.ValueCents)
				assert.EqualValues(t, 1_000_000_000, p.DeliveryFeeCents)
				return orderRowFrom(p), nil
			})

		rec := send(t, h, http.MethodPost, orderPath, orderWith("10000000", "10000000"))

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		order := decode(t, rec)["order"].(map[string]any)
		assert.Equal(t, 10_000_000.0, order["value_kes"])
		assert.Equal(t, 9_000_000.0, order["estimated_earnings_kes"], "90% of the maximum fee, exact in cents")
	})

	tests := []struct{ name, value, fee, wantField string }{
		{"value above the maximum", "10000000.01", "250", "value_kes"},
		{"fee above the maximum", "5000", "10000000.01", "delivery_fee_kes"},
		{"an absurd value", "1e300", "250", "value_kes"},
		{"a value with a third decimal", "10.005", "250", "value_kes"},
		{"a fee with a third decimal", "5000", "0.001", "delivery_fee_kes"},
		{"zero value", "0", "250", "value_kes"},
		{"negative fee", "5000", "-0.01", "delivery_fee_kes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // rejected before any store call

			rec := send(t, h, http.MethodPost, orderPath, orderWith(tc.value, tc.fee))

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}

	t.Run("the message names the limit", func(t *testing.T) {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPost, orderPath, orderWith("10000001", "250"))

		assert.Equal(t, "must not be more than 10000000 KES", errFields(t, rec)["value_kes"])
	})

	t.Run("19.99 is stored as 1999 cents", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetVendor(gomock.Any(), gomock.Any()).Return(fakeVendor(), nil)
		store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
		store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) {
				assert.EqualValues(t, 1999, p.ValueCents)
				assert.EqualValues(t, 30, p.DeliveryFeeCents, "0.1 + 0.2 typed as a fee is 30 cents")
				return orderRowFrom(p), nil
			})

		rec := send(t, h, http.MethodPost, orderPath, orderWith("19.99", "0.30"))

		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})
}

func TestMatchRidersAndPreferences_UseTheSameValueBound(t *testing.T) {
	t.Run("match-riders value above the maximum", func(t *testing.T) {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPost, matchRidersPath, `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":10000001}}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, errFields(t, rec), "order_details.value_kes")
	})
	t.Run("a minimum order value above the maximum", func(t *testing.T) {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", `{"min_order_value_kes":10000001}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, errFields(t, rec)["min_order_value_kes"], "must not be more than 10000000")
	})
}
