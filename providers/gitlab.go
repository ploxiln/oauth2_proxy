package providers

import (
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/bitly/oauth2_proxy/api"
)

type GitLabProvider struct {
	*ProviderData
	Groups []string
}

func NewGitLabProvider(p *ProviderData) *GitLabProvider {
	p.ProviderName = "GitLab"
	if p.LoginURL == nil || p.LoginURL.String() == "" {
		p.LoginURL = &url.URL{
			Scheme: "https",
			Host:   "gitlab.com",
			Path:   "/oauth/authorize",
		}
	}
	if p.RedeemURL == nil || p.RedeemURL.String() == "" {
		p.RedeemURL = &url.URL{
			Scheme: "https",
			Host:   "gitlab.com",
			Path:   "/oauth/token",
		}
	}
	if p.ValidateURL == nil || p.ValidateURL.String() == "" {
		p.ValidateURL = &url.URL{
			Scheme: "https",
			Host:   "gitlab.com",
			Path:   "/api/v4/user",
		}
	}
	if p.Scope == "" {
		p.Scope = "read_user"
	}
	return &GitLabProvider{ProviderData: p}
}

func (p *GitLabProvider) SetGroups(groups []string) {
	p.Groups = groups
	if len(groups) > 0 {
		p.Scope = "api"
	}
}

func (p *GitLabProvider) hasGroup(accessToken string) (bool, error) {

	type groupsPage []struct {
		FullPath string `json:"full_path"`
	}

	pn := 1
	for {
		params := url.Values{
			"access_token": {accessToken},
			"per_page":     {"100"},
			"page":         {strconv.Itoa(pn)},
		}

		endpoint := &url.URL{
			Scheme:   p.ValidateURL.Scheme,
			Host:     p.ValidateURL.Host,
			Path:     path.Join(p.ValidateURL.Path, "../groups"),
			RawQuery: params.Encode(),
		}
		req, err := http.NewRequest("GET", endpoint.String(), nil)
		if err != nil {
			return false, err
		}

		var groups groupsPage
		err = api.RequestJson(req, &groups)
		if err != nil {
			return false, err
		}
		if len(groups) == 0 {
			break
		}

		for _, group := range groups {
			for _, g := range p.Groups {
				if g == group.FullPath {
					log.Printf("Found GitLab Group:%q", g)
					return true, nil
				}
			}
		}

		pn += 1
	}

	return false, nil
}

func (p *GitLabProvider) GetEmailAddress(s *SessionState) (string, error) {
	// if we require a Group, check that first
	if len(p.Groups) > 0 {
		if ok, err := p.hasGroup(s.AccessToken); err != nil || !ok {
			return "", err
		}
	}

	req, err := http.NewRequest("GET",
		p.ValidateURL.String()+"?access_token="+s.AccessToken, nil)
	if err != nil {
		log.Printf("failed building request %s", err)
		return "", err
	}
	json, err := api.Request(req)
	if err != nil {
		log.Printf("failed making request %s", err)
		return "", err
	}
	return json.Get("email").String()
}
