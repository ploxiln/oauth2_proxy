package providers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ploxiln/oauth2_proxy/api"
)

type DiscordProvider struct {
	*ProviderData
}

type DiscordUserInfo struct {
    Id              string  `json:"id"`
    Avatar          string  `json:"avatar"`
    Username        string  `json:"username"`
    Discriminator   string  `json:"discriminator"`
    Verified        bool    `json:"verified"`
    Email           string  `json:"email"`
    Flags           int     `json:"flags"`
    Locale          string  `json:"locale"`
    PremiumType     int     `json:"premium_type"`
    MfaEnabled      bool    `json:"mfa_enabled"`
}

func NewDiscordProvider(p *ProviderData) *DiscordProvider {
	p.ProviderName = "Discord"
	if p.LoginURL.String() == "" {
		p.LoginURL = &url.URL{Scheme: "https",
			Host: "discordapp.com",
			Path: "/api/oauth2/authorize",
		}
	}
	if p.RedeemURL.String() == "" {
		p.RedeemURL = &url.URL{Scheme: "https",
			Host: "discordapp.com",
			Path: "/api/oauth2/token",
		}
	}
	if p.ProfileURL.String() == "" {
		p.ProfileURL = &url.URL{Scheme: "https",
			Host: "discordapp.com",
			Path: "/api/users/@me",
		}
	}
	if p.ValidateURL.String() == "" {
		p.ValidateURL = p.ProfileURL
	}
	if p.Scope == "" {
		p.Scope = "identify email connections"
	}
	return &DiscordProvider{ProviderData: p}
}

func getDiscordHeader(access_token string) http.Header {
	header := make(http.Header)
	header.Set("Accept", "application/json")
	header.Set("Authorization", fmt.Sprintf("Bearer %s", access_token))
	return header
}

func getUserInfo(p *DiscordProvider, s *SessionState) (DiscordUserInfo, error) {
	var r DiscordUserInfo
	if s.AccessToken == "" {
		return r, errors.New("missing access token")
	}
	req, err := http.NewRequest("GET", p.ProfileURL.String(), nil)
	if err != nil {
		return r, err
	}
	req.Header = getDiscordHeader(s.AccessToken)

	err = api.RequestJson(req, &r)
	if err != nil {
		return r, err
	}
	return r, nil
}

// NOTE: This does NOT expose the username+discriminator combo for the
// authenticated user, as this is NOT STABLE and can be changed at any
// time! Instead, the user id which is guratanteed to be stable by
// Discord is provided.
func (p *DiscordProvider) GetUserName(s *SessionState) (string, error) {
    r, err := getUserInfo(p, s)
	if err != nil {
		return "", err
	}
	if r.Email == "" {
		return "", errors.New("no email")
	}
	return r.Id, nil
}

func (p *DiscordProvider) GetEmailAddress(s *SessionState) (string, error) {
    r, err := getUserInfo(p, s)
	if err != nil {
		return "", err
	}
	if r.Email == "" {
		return "", errors.New("no email")
	}
	return r.Email, nil
}

func (p *DiscordProvider) ValidateSessionState(s *SessionState) bool {
	return validateToken(p, s.AccessToken, getDiscordHeader(s.AccessToken))
}
