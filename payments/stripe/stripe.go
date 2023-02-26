package stripe

import (
	"context"
	"fmt"
	"net/http"

	"encoding/json"

	"github.com/netlify/gocommerce/models"
	"github.com/netlify/gocommerce/payments"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	stripe "github.com/stripe/stripe-go"
	"github.com/stripe/stripe-go/client"
)

type stripePaymentProvider struct {
	client *client.API
}

type stripeBodyParams struct {
	StripeToken           string `json:"stripe_token"`
	StripePaymentMethodID string `json:"stripe_payment_method_id"`
}

// Config contains the Stripe-specific configuration for payment providers.
type Config struct {
	SecretKey string `mapstructure:"secret_key" json:"secret_key"`
}

// NewPaymentProvider creates a new Stripe payment provider using the provided configuration.
func NewPaymentProvider(config Config) (payments.Provider, error) {
	if config.SecretKey == "" {
		return nil, errors.New("Stripe configuration missing secret_key")
	}

	s := stripePaymentProvider{
		client: &client.API{},
	}
	s.client.Init(config.SecretKey, nil)
	return &s, nil
}

func (s *stripePaymentProvider) Name() string {
	return payments.StripeProvider
}

func (s *stripePaymentProvider) NewCharger(ctx context.Context, r *http.Request, log logrus.FieldLogger) (payments.Charger, error) {
	var bp stripeBodyParams
	bod, err := r.GetBody()
	if err != nil {
		return nil, err
	}
	err = json.NewDecoder(bod).Decode(&bp)
	if err != nil {
		return nil, err
	}

	if bp.StripePaymentMethodID == "" {
		return nil, errors.New("Stripe requires a stripe_payment_method_id for creating a payment intent")
	}
	return func(amount uint64, currency string, order *models.Order, invoiceNumber int64) (string, error) {
		return s.chargePaymentIntent(bp.StripePaymentMethodID, amount, currency, order, invoiceNumber)
	}, nil
}

func prepareShippingAddress(addr models.Address) *stripe.ShippingDetailsParams {
	return &stripe.ShippingDetailsParams{
		Address: &stripe.AddressParams{
			Line1:      &addr.Address1,
			Line2:      &addr.Address2,
			City:       &addr.City,
			State:      &addr.State,
			PostalCode: &addr.Zip,
			Country:    &addr.Country,
		},
		Name: &addr.Name,
	}
}

func (s *stripePaymentProvider) chargePaymentIntent(paymentMethodID string, amount uint64, currency string, order *models.Order, invoiceNumber int64) (string, error) {
	params := &stripe.PaymentIntentParams{
		PaymentMethod: stripe.String(paymentMethodID),
		Amount:        stripe.Int64(int64(amount)),
		Currency:      stripe.String(currency),
		Description:   stripe.String(fmt.Sprintf("Invoice No. %d", invoiceNumber)),
		Shipping:      prepareShippingAddress(order.ShippingAddress),
		Params: stripe.Params{
			Metadata: map[string]string{
				"order_id":       order.ID,
				"invoice_number": fmt.Sprintf("%d", invoiceNumber),
			},
		},
		ConfirmationMethod: stripe.String(string(
			stripe.PaymentIntentConfirmationMethodManual,
		)),
		Confirm: stripe.Bool(true),
	}
	intent, err := s.client.PaymentIntents.New(params)
	if err != nil {
		return "", err
	}

	if intent.Status == stripe.PaymentIntentStatusRequiresAction {
		return intent.ID, payments.NewPaymentPendingError(map[string]interface{}{
			"payment_intent_secret": intent.ClientSecret,
		})
	}

	if intent.Status == stripe.PaymentIntentStatusSucceeded {
		return intent.ID, nil
	}

	return "", fmt.Errorf("Invalid PaymentIntent status: %s", intent.Status)
}

func (s *stripePaymentProvider) NewRefunder(ctx context.Context, r *http.Request, log logrus.FieldLogger) (payments.Refunder, error) {
	return s.refund, nil
}

func (s *stripePaymentProvider) refund(transactionID string, amount uint64, currency string) (string, error) {
	stripeAmount := int64(amount)
	ref, err := s.client.Refunds.New(&stripe.RefundParams{
		Charge: &transactionID,
		Amount: &stripeAmount,
	})
	if err != nil {
		return "", err
	}

	return ref.ID, err
}

func (s *stripePaymentProvider) NewPreauthorizer(ctx context.Context, r *http.Request, log logrus.FieldLogger) (payments.Preauthorizer, error) {
	return nil, errors.New("Stripe does not require preauthorization")
}

func (s *stripePaymentProvider) NewConfirmer(ctx context.Context, r *http.Request, log logrus.FieldLogger) (payments.Confirmer, error) {
	return s.confirm, nil
}

func (s *stripePaymentProvider) confirm(paymentID string) error {
	_, err := s.client.PaymentIntents.Confirm(paymentID, nil)

	if stripeErr, ok := err.(*stripe.Error); ok {
		return payments.NewPaymentConfirmFailError(stripeErr.Msg)
	}

	return err
}
