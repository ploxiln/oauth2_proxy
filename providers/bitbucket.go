package providers

import (
	"log"
	"net/http"
	"net/url"

	"github.com/ploxiln/oauth2_proxy/api"
)

type BitbucketProvider struct {
	*ProviderData
	Team string
}

func NewBitbucketProvider(p *ProviderData) *BitbucketProvider {
	p.ProviderName = "Bitbucket"
	if p.LoginURL == nil || p.LoginURL.String() == "" {
		p.LoginURL = &url.URL{
			Scheme: "https",
			Host:   "bitbucket.org",
			Path:   "/site/oauth2/authorize",
		}
	}
	if p.RedeemURL == nil || p.RedeemURL.String() == "" {
		p.RedeemURL = &url.URL{
			Scheme: "https",
			Host:   "bitbucket.org",
			Path:   "/site/oauth2/access_token",
		}
	}
	if p.ValidateURL == nil || p.ValidateURL.String() == "" {
		p.ValidateURL = &url.URL{
			Scheme: "https",
			Host:   "api.bitbucket.org",
			Path:   "/2.0/user/emails",
		}
	}
	if p.Scope == "" {
		p.Scope = "account team"
	}
	return &BitbucketProvider{ProviderData: p}
}

func (p *BitbucketProvider) SetTeam(team string) {
	p.Team = team
}

func (p *BitbucketProvider) GetEmailAddress(s *SessionState) (string, error) {

	var emails struct {
		Values []struct {
			Email   string `json:"email"`
			Primary bool   `json:"is_primary"`
		}
	}
	var teams struct {
		Values []struct {
			Name string `json:"username"`
		}
	}
	req, err := http.NewRequest("GET",
		p.ValidateURL.String()+"?access_token="+s.AccessToken, nil)
	if err != nil {
		log.Printf("failed building request %s", err)
		return "", err
	}
	err = api.RequestJson(req, &emails)
	if err != nil {
		log.Printf("failed making request %s", err)
		return "", err
	}

	if p.Team != "" {
		teamURL := &url.URL{}
		*teamURL = *p.ValidateURL
		teamURL.Path = "/2.0/teams"
		req, err = http.NewRequest("GET",
			teamURL.String()+"?role=member&access_token="+s.AccessToken, nil)
		if err != nil {
			log.Printf("failed building request %s", err)
			return "", err
		}
		err = api.RequestJson(req, &teams)
		if err != nil {
			log.Printf("failed requesting teams membership %s", err)
			return "", err
		}
		var found = false
		for _, team := range teams.Values {
			if p.Team == team.Name {
				found = true
				break
			}
		}
		if found != true {
			log.Printf("team membership test failed, access denied")
			return "", nil
		}
	}

	for _, email := range emails.Values {
		if email.Primary {
			return email.Email, nil
		}
	}

	return "", nil
}
