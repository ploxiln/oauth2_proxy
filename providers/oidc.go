package providers

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"golang.org/x/oauth2"

	oidc "github.com/coreos/go-oidc"
)

type OIDCProvider struct {
	*ProviderData

	Verifier *oidc.IDTokenVerifier
}

func NewOIDCProvider(p *ProviderData) *OIDCProvider {
	p.ProviderName = "OpenID Connect"
	return &OIDCProvider{ProviderData: p}
}

func (p *OIDCProvider) SetIssuerURL(issuerURL string) error {
	provider, err := oidc.NewProvider(context.Background(), issuerURL)
	if err != nil {
		return fmt.Errorf("error looking up issuer-url=%q %s", issuerURL, err)
	}
	p.Verifier = provider.Verifier(&oidc.Config{
		ClientID: p.ClientID,
	})
	p.LoginURL, err = url.Parse(provider.Endpoint().AuthURL)
	if err != nil {
		return fmt.Errorf("error parsing login-url=%q %s", provider.Endpoint().AuthURL, err)
	}
	p.RedeemURL, err = url.Parse(provider.Endpoint().TokenURL)
	if err != nil {
		return fmt.Errorf("error parsing redeem-url=%q %s", provider.Endpoint().TokenURL, err)
	}
	if p.Scope == "" {
		p.Scope = "openid email profile"
	}
	return nil
}

func (p *OIDCProvider) SetVerifier(issuerURL string, jwksURL string) {
	keySet := oidc.NewRemoteKeySet(context.Background(), jwksURL)
	p.Verifier = oidc.NewVerifier(issuerURL, keySet, &oidc.Config{
		ClientID: p.ClientID,
	})
}

func (p *OIDCProvider) Redeem(redirectURL, code string) (s *SessionState, err error) {
	ctx := context.Background()
	c := oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: p.RedeemURL.String(),
		},
		RedirectURL: redirectURL,
	}
	token, err := c.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %v", err)
	}
	s, err = p.createSessionState(token, ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update session: %v", err)
	}
	return
}

func (p *OIDCProvider) RefreshSessionIfNeeded(s *SessionState) (bool, error) {
	if s == nil || s.ExpiresOn.After(time.Now()) || s.RefreshToken == "" {
		return false, nil
	}

	origExpiration := s.ExpiresOn

	err := p.redeemRefreshToken(s)
	if err != nil {
		return false, fmt.Errorf("unable to redeem refresh token: %v", err)
	}

	fmt.Printf("refreshed id token %s (expired on %s)\n", s, origExpiration)
	return true, nil
}

func (p *OIDCProvider) redeemRefreshToken(s *SessionState) (err error) {
	c := oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: p.RedeemURL.String(),
		},
	}
	ctx := context.Background()
	t := &oauth2.Token{
		RefreshToken: s.RefreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	}
	token, err := c.TokenSource(ctx, t).Token()
	if err != nil {
		return fmt.Errorf("failed to get token: %v", err)
	}
	newSession, err := p.createSessionState(token, ctx)
	if err != nil {
		return fmt.Errorf("unable to update session: %v", err)
	}
	s.AccessToken = newSession.AccessToken
	s.RefreshToken = newSession.RefreshToken
	s.ExpiresOn = newSession.ExpiresOn
	s.Email = newSession.Email
	return
}

func (p *OIDCProvider) createSessionState(token *oauth2.Token, ctx context.Context) (*SessionState, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("token response did not contain an id_token")
	}

	// Parse and verify ID Token payload.
	idToken, err := p.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("could not verify id_token: %v", err)
	}

	// Extract custom claims.
	var claims struct {
		Subject  string `json:"sub"`
		Email    string `json:"email"`
		Verified *bool  `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %v", err)
	}

	if claims.Email == "" {
		// "sub" is mandatory but "email" is not
		// TODO: Try getting email from /userinfo before falling back to Subject
		claims.Email = claims.Subject
	}
	if claims.Verified != nil && !*claims.Verified {
		return nil, fmt.Errorf("email in id_token (%s) isn't verified", claims.Email)
	}

	return &SessionState{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresOn:    token.Expiry,
		Email:        claims.Email,
	}, nil
}
