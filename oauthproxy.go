package main

import (
	"crypto/tls"
	b64 "encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mbland/hmacauth"
	"github.com/ploxiln/oauth2_proxy/cookie"
	"github.com/ploxiln/oauth2_proxy/providers"
	"github.com/yhat/wsutil"
)

const SignatureHeader = "GAP-Signature"

var SignatureHeaders []string = []string{
	"Content-Length",
	"Content-Md5",
	"Content-Type",
	"Date",
	"Authorization",
	"X-Forwarded-User",
	"X-Forwarded-Email",
	"X-Forwarded-Access-Token",
	"Cookie",
	"Gap-Auth",
}

type OAuthProxy struct {
	CookieSeed     string
	CookieName     string
	CSRFCookieName string
	CookieDomain   string
	CookiePath     string
	CookieSameSite http.SameSite
	CookieSecure   bool
	CookieHttpOnly bool
	CookieExpire   time.Duration
	CookieRefresh  time.Duration
	Validator      func(string) bool

	RobotsPath        string
	PingPath          string
	SignInPath        string
	SignOutPath       string
	OAuthStartPath    string
	OAuthCallbackPath string
	AuthOnlyPath      string

	redirectURL         *url.URL // the url to receive requests at
	whitelistDomains    []string
	provider            providers.Provider
	ProxyPrefix         string
	SignInMessage       string
	HtpasswdFile        *HtpasswdFile
	DisplayHtpasswdForm bool
	serveMux            http.Handler
	SetXAuthRequest     bool
	PassBasicAuth       bool
	SkipProviderButton  bool
	PassUserHeaders     bool
	BasicAuthPassword   string
	PassAccessToken     bool
	ClientIPHeader      string
	CookieCipher        *cookie.Cipher
	skipAuthRegex       []string
	skipAuthStripHdrs   bool
	skipAuthPreflight   bool
	compiledRegex       []*regexp.Regexp
	templates           *template.Template
	Footer              string
}

type UpstreamProxy struct {
	upstream  string
	handler   http.Handler
	wsHandler http.Handler
	auth      hmacauth.HmacAuth
}

func (u *UpstreamProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("GAP-Upstream-Address", u.upstream)
	if u.auth != nil {
		r.Header.Set("GAP-Auth", w.Header().Get("GAP-Auth"))
		u.auth.SignRequest(r)
	}
	if u.wsHandler != nil && strings.ToLower(r.Header.Get("Connection")) == "upgrade" && r.Header.Get("Upgrade") == "websocket" {
		u.wsHandler.ServeHTTP(w, r)
	} else {
		u.handler.ServeHTTP(w, r)
	}

}

func setProxyUpstreamHostHeader(proxy *httputil.ReverseProxy, target *url.URL) {
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		// use RequestURI so that we aren't unescaping encoded slashes in the request path
		req.Host = target.Host
		req.URL.Opaque = req.RequestURI
		req.URL.RawQuery = ""
	}
}
func setProxyDirector(proxy *httputil.ReverseProxy) {
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		// use RequestURI so that we aren't unescaping encoded slashes in the request path
		req.URL.Opaque = req.RequestURI
		req.URL.RawQuery = ""
	}
}
func NewFileServer(path string, filesystemPath string) (proxy http.Handler) {
	return http.StripPrefix(path, http.FileServer(http.Dir(filesystemPath)))
}

func NewWebSocketOrRestReverseProxy(u *url.URL, opts *Options, auth hmacauth.HmacAuth) (restProxy http.Handler) {
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.FlushInterval = opts.FlushInterval

	u.Path = ""
	if !opts.PassHostHeader {
		setProxyUpstreamHostHeader(proxy, u)
	} else {
		setProxyDirector(proxy)
	}

	// this should give us a wss:// scheme if the url is https:// based.
	var wsProxy *wsutil.ReverseProxy
	if opts.ProxyWebSockets {
		wsScheme := "ws" + strings.TrimPrefix(u.Scheme, "http")
		wsURL := &url.URL{Scheme: wsScheme, Host: u.Host}
		wsProxy = wsutil.NewSingleHostReverseProxy(wsURL)
		if opts.SSLInsecureSkipVerify {
			wsProxy.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}
	return &UpstreamProxy{u.Host, proxy, wsProxy, auth}
}

func preventCaching(rw http.ResponseWriter) {
	rw.Header().Set("Cache-Control", "no-store")
}

func NewOAuthProxy(opts *Options, validator func(string) bool) *OAuthProxy {
	serveMux := http.NewServeMux()
	var auth hmacauth.HmacAuth
	if sigData := opts.signatureData; sigData != nil {
		auth = hmacauth.NewHmacAuth(sigData.hash, []byte(sigData.key),
			SignatureHeader, SignatureHeaders)
	}
	for _, u := range opts.proxyURLs {
		path := u.Path
		switch u.Scheme {
		case "http", "https":
			log.Printf("mapping path %q => upstream %q", path, u)
			proxy := NewWebSocketOrRestReverseProxy(u, opts, auth)
			serveMux.Handle(path, proxy)
		case "file":
			if u.Fragment != "" {
				path = u.Fragment
			}
			log.Printf("mapping path %q => file system %q", path, u.Path)
			proxy := NewFileServer(path, u.Path)
			serveMux.Handle(path, &UpstreamProxy{path, proxy, nil, nil})
		default:
			panic(fmt.Sprintf("unknown upstream protocol %s", u.Scheme))
		}
	}
	for _, u := range opts.CompiledRegex {
		log.Printf("compiled skip-auth-regex => %q", u)
	}

	redirectURL := opts.redirectURL
	if redirectURL.Path == "" {
		redirectURL.Path = fmt.Sprintf("%s/callback", opts.ProxyPrefix)
	}

	log.Printf("OAuthProxy configured for %s Client ID: %s", opts.provider.Data().ProviderName, opts.ClientID)
	refresh := "disabled"
	if opts.CookieRefresh != time.Duration(0) {
		refresh = fmt.Sprintf("after %s", opts.CookieRefresh)
	}

	log.Printf("Cookie settings: name=%s domain=%s path=%s httponly=%v secure(https)=%v samesite=%s expiry=%s refresh=%s",
		opts.CookieName, opts.CookieDomain, opts.CookiePath,
		opts.CookieHttpOnly, opts.CookieSecure, opts.CookieSameSite,
		opts.CookieExpire, refresh)

	var cipher *cookie.Cipher
	if opts.PassAccessToken || (opts.CookieRefresh != time.Duration(0)) {
		var err error
		cipher, err = cookie.NewCipher(secretBytes(opts.CookieSecret))
		if err != nil {
			log.Fatal("cookie-secret error: ", err)
		}
	}

	return &OAuthProxy{
		CookieName:     opts.CookieName,
		CSRFCookieName: fmt.Sprintf("%v_%v", opts.CookieName, "csrf"),
		CookieSeed:     opts.CookieSecret,
		CookieDomain:   opts.CookieDomain,
		CookiePath:     opts.CookiePath,
		CookieSecure:   opts.CookieSecure,
		CookieHttpOnly: opts.CookieHttpOnly,
		CookieExpire:   opts.CookieExpire,
		CookieRefresh:  opts.CookieRefresh,
		CookieSameSite: parseSameSite(opts.CookieSameSite),
		Validator:      validator,

		RobotsPath:        "/robots.txt",
		PingPath:          "/ping",
		SignInPath:        fmt.Sprintf("%s/sign_in", opts.ProxyPrefix),
		SignOutPath:       fmt.Sprintf("%s/sign_out", opts.ProxyPrefix),
		OAuthStartPath:    fmt.Sprintf("%s/start", opts.ProxyPrefix),
		OAuthCallbackPath: fmt.Sprintf("%s/callback", opts.ProxyPrefix),
		AuthOnlyPath:      fmt.Sprintf("%s/auth", opts.ProxyPrefix),

		ProxyPrefix:        opts.ProxyPrefix,
		provider:           opts.provider,
		serveMux:           serveMux,
		redirectURL:        redirectURL,
		whitelistDomains:   opts.WhitelistDomains,
		skipAuthRegex:      opts.SkipAuthRegex,
		skipAuthStripHdrs:  opts.SkipAuthStripHeaders,
		skipAuthPreflight:  opts.SkipAuthPreflight,
		compiledRegex:      opts.CompiledRegex,
		SetXAuthRequest:    opts.SetXAuthRequest,
		PassBasicAuth:      opts.PassBasicAuth,
		PassUserHeaders:    opts.PassUserHeaders,
		BasicAuthPassword:  opts.BasicAuthPassword,
		PassAccessToken:    opts.PassAccessToken,
		SkipProviderButton: opts.SkipProviderButton,
		ClientIPHeader:     opts.RealClientIPHeader,
		CookieCipher:       cipher,
		templates:          loadTemplates(opts.CustomTemplatesDir),
		Footer:             opts.Footer,
	}
}

func (p *OAuthProxy) GetRedirectURI(host string) string {
	// default to the request Host if not set
	if p.redirectURL.Host != "" {
		return p.redirectURL.String()
	}
	var u url.URL
	u = *p.redirectURL
	if u.Scheme == "" {
		if p.CookieSecure {
			u.Scheme = "https"
		} else {
			u.Scheme = "http"
		}
	}
	u.Host = host
	return u.String()
}

func (p *OAuthProxy) displayCustomLoginForm() bool {
	return p.HtpasswdFile != nil && p.DisplayHtpasswdForm
}

func (p *OAuthProxy) redeemCode(host, code string) (s *providers.SessionState, err error) {
	if code == "" {
		return nil, errors.New("missing code")
	}
	redirectURI := p.GetRedirectURI(host)
	s, err = p.provider.Redeem(redirectURI, code)
	if err != nil {
		return
	}

	if s.Email == "" {
		s.Email, err = p.provider.GetEmailAddress(s)
	}

	if s.User == "" {
		s.User, err = p.provider.GetUserName(s)
		if err != nil && err.Error() == "not implemented" {
			err = nil
		}
	}
	return
}

func (p *OAuthProxy) MakeSessionCookie(req *http.Request, value string, expiration time.Duration, now time.Time) *http.Cookie {
	if value != "" {
		value = cookie.SignedValue(p.CookieSeed, p.CookieName, value, now)
	}
	return p.makeCookie(req, p.CookieName, value, expiration, now)
}

func (p *OAuthProxy) MakeCSRFCookie(req *http.Request, value string, expiration time.Duration, now time.Time) *http.Cookie {
	return p.makeCookie(req, p.CSRFCookieName, value, expiration, now)
}

func (p *OAuthProxy) makeCookie(req *http.Request, name string, value string, expiration time.Duration, now time.Time) *http.Cookie {
	if p.CookieDomain != "" {
		domain := req.Host
		if h, _, err := net.SplitHostPort(domain); err == nil {
			domain = h
		}
		if !strings.HasSuffix(domain, p.CookieDomain) {
			log.Printf("Warning: request host is %q but using configured cookie domain of %q", domain, p.CookieDomain)
		}
	}
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     p.CookiePath,
		Domain:   p.CookieDomain,
		SameSite: p.CookieSameSite,
		HttpOnly: p.CookieHttpOnly,
		Secure:   p.CookieSecure,
		Expires:  now.Add(expiration),
	}
	if len(cookie.String()) > 4000 {
		// may exceed nginx default header limit and browser per-cookie limit
		log.Printf("WARNING: cookie %q may be too big: %d bytes", name, len(value))
	}
	return cookie
}

func parseSameSite(v string) http.SameSite {
	switch v {
	case "lax":
		return http.SameSiteLaxMode
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	case "":
		return http.SameSiteDefaultMode
	default:
		panic(fmt.Sprintf("Invalid value for SameSite: %s", v))
	}
}

func (p *OAuthProxy) ClearCSRFCookie(rw http.ResponseWriter, req *http.Request) {
	http.SetCookie(rw, p.MakeCSRFCookie(req, "", time.Hour*-1, time.Now()))
}

func (p *OAuthProxy) SetCSRFCookie(rw http.ResponseWriter, req *http.Request, val string) {
	http.SetCookie(rw, p.MakeCSRFCookie(req, val, p.CookieExpire, time.Now()))
}

func (p *OAuthProxy) ClearSessionCookie(rw http.ResponseWriter, req *http.Request) {
	clr := p.MakeSessionCookie(req, "", time.Hour*-1, time.Now())
	http.SetCookie(rw, clr)

	// ugly hack because default domain changed
	if p.CookieDomain == "" {
		clr2 := *clr
		clr2.Domain = req.Host
		http.SetCookie(rw, &clr2)
	}
}

func (p *OAuthProxy) SetSessionCookie(rw http.ResponseWriter, req *http.Request, val string) {
	http.SetCookie(rw, p.MakeSessionCookie(req, val, p.CookieExpire, time.Now()))
}

func (p *OAuthProxy) LoadCookiedSession(req *http.Request) (*providers.SessionState, time.Duration, error) {
	var age time.Duration
	c, err := req.Cookie(p.CookieName)
	if err != nil {
		// always http.ErrNoCookie
		return nil, age, fmt.Errorf("Cookie %q not present", p.CookieName)
	}
	val, timestamp, ok := cookie.Validate(c, p.CookieSeed, p.CookieExpire)
	if !ok {
		return nil, age, errors.New("Cookie Signature not valid")
	}

	session, err := p.provider.SessionFromCookie(val, p.CookieCipher)
	if err != nil {
		return nil, age, err
	}

	age = time.Now().Truncate(time.Second).Sub(timestamp)
	return session, age, nil
}

func (p *OAuthProxy) SaveSession(rw http.ResponseWriter, req *http.Request, s *providers.SessionState) error {
	value, err := p.provider.CookieForSession(s, p.CookieCipher)
	if err != nil {
		return err
	}
	p.SetSessionCookie(rw, req, value)
	return nil
}

func (p *OAuthProxy) stripAuthHeaders(req *http.Request) {
	if !p.skipAuthStripHdrs {
		return
	}
	if p.PassBasicAuth {
		req.Header.Del("Authorization")
	}
	if p.PassUserHeaders {
		req.Header.Del("X-Forwarded-User")
		req.Header.Del("X-Forwarded-Email")
	}
	if p.PassAccessToken {
		req.Header.Del("X-Forwarded-Access-Token")
	}
}

func (p *OAuthProxy) RobotsTxt(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusOK)
	fmt.Fprintf(rw, "User-agent: *\nDisallow: /")
}

func (p *OAuthProxy) PingPage(rw http.ResponseWriter) {
	preventCaching(rw)
	rw.WriteHeader(http.StatusOK)
	fmt.Fprintf(rw, "OK")
}

func (p *OAuthProxy) ErrorPage(rw http.ResponseWriter, code int, title string, message string) {
	log.Printf("ErrorPage %d %s %s", code, title, message)
	rw.WriteHeader(code)
	t := struct {
		Title       string
		Message     string
		ProxyPrefix string
	}{
		Title:       fmt.Sprintf("%d %s", code, title),
		Message:     message,
		ProxyPrefix: p.ProxyPrefix,
	}
	p.templates.ExecuteTemplate(rw, "error.html", t)
}

func (p *OAuthProxy) SignInPage(rw http.ResponseWriter, req *http.Request, code int) {
	preventCaching(rw)
	p.ClearSessionCookie(rw, req)
	rw.WriteHeader(code)

	redirect_url, err := p.GetRedirect(req)
	if err != nil {
		p.ErrorPage(rw, 400, "Bad Request", err.Error())
		return
	}

	t := struct {
		ProviderName  string
		SignInMessage template.HTML
		CustomLogin   bool
		Redirect      string
		Version       string
		ProxyPrefix   string
		Footer        template.HTML
	}{
		ProviderName:  p.provider.Data().ProviderName,
		SignInMessage: template.HTML(p.SignInMessage),
		CustomLogin:   p.displayCustomLoginForm(),
		Redirect:      redirect_url,
		Version:       VERSION,
		ProxyPrefix:   p.ProxyPrefix,
		Footer:        template.HTML(p.Footer),
	}
	p.templates.ExecuteTemplate(rw, "sign_in.html", t)
}

func (p *OAuthProxy) ManualSignIn(rw http.ResponseWriter, req *http.Request) (string, bool) {
	if req.Method != "POST" || p.HtpasswdFile == nil {
		return "", false
	}
	user := req.FormValue("username")
	passwd := req.FormValue("password")
	if user == "" {
		return "", false
	}
	// check auth
	if p.HtpasswdFile.Validate(user, passwd) {
		log.Printf("authenticated %q via HtpasswdFile", user)
		return user, true
	}
	return "", false
}

func (p *OAuthProxy) GetRedirect(req *http.Request) (redirect string, err error) {
	err = req.ParseForm()
	if err != nil {
		return
	}
	redirect = req.Form.Get("rd")
	if redirect == "" {
		redirect = req.Header.Get("X-Auth-Request-Redirect")
	}
	if redirect == "" {
		redirect = req.URL.RequestURI()
	}
	if !p.IsValidRedirect(redirect) {
		redirect = "/"
	}
	return
}

var invalidRedirectRe = regexp.MustCompile(`[/\\](?:[\s\v]*|\.\.?)[/\\]`)

func (p *OAuthProxy) IsValidRedirect(redirect string) bool {
	url, err := url.Parse(redirect)
	if err != nil {
		log.Printf("invalid redirect: failed to parse url: %q", redirect)
		return false
	}
	if url.Path == p.SignInPath || url.Path == p.OAuthStartPath {
		log.Printf("invalid redirect: is auth start or sign_in path: %q", redirect)
		return false
	}
	if strings.HasPrefix(redirect, "/") && invalidRedirectRe.MatchString(redirect) {
		// prevent open-redirect tricks: `//` or `/\` or `/ /` or `/ \` or `/./\\` etc.
		log.Printf("invalid redirect: tricky prefix: %q", redirect)
		return false
	}
	if strings.HasPrefix(redirect, "/") {
		return true
	}
	if strings.HasPrefix(redirect, "http://") || strings.HasPrefix(redirect, "https://") {
		for _, domain := range p.whitelistDomains {
			if url.Host == domain {
				return true
			}
			if strings.HasPrefix(domain, ".") && strings.HasSuffix(url.Host, domain) {
				return true
			}
		}
		log.Printf("invalid redirect: domain not in whitelist: %q", redirect)
	}
	return false
}

func (p *OAuthProxy) IsWhitelistedRequest(req *http.Request) (ok bool) {
	isPreflightRequestAllowed := p.skipAuthPreflight && req.Method == "OPTIONS"
	return isPreflightRequestAllowed || p.IsWhitelistedPath(req.URL.Path)
}

func (p *OAuthProxy) IsWhitelistedPath(path string) (ok bool) {
	for _, u := range p.compiledRegex {
		ok = u.MatchString(path)
		if ok {
			return
		}
	}
	return
}

func (p *OAuthProxy) getRemoteAddr(req *http.Request) (s string) {
	s = req.RemoteAddr

	hval := extractClientIP(req, p.ClientIPHeader)
	if hval != "" {
		s += fmt.Sprintf(" (%q)", hval)
	}
	return
}

func (p *OAuthProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	switch path := req.URL.Path; {
	case path == p.RobotsPath:
		p.RobotsTxt(rw)
	case path == p.PingPath:
		p.PingPage(rw)
	case p.IsWhitelistedRequest(req):
		p.stripAuthHeaders(req)
		p.serveMux.ServeHTTP(rw, req)
	case path == p.SignInPath:
		p.SignIn(rw, req)
	case path == p.SignOutPath:
		p.SignOut(rw, req)
	case path == p.OAuthStartPath:
		p.OAuthStart(rw, req)
	case path == p.OAuthCallbackPath:
		p.OAuthCallback(rw, req)
	case path == p.AuthOnlyPath:
		p.AuthenticateOnly(rw, req)
	default:
		p.Proxy(rw, req)
	}
}

func (p *OAuthProxy) SignIn(rw http.ResponseWriter, req *http.Request) {
	user, ok := p.ManualSignIn(rw, req)
	if ok {
		redirect, err := p.GetRedirect(req)
		if err != nil {
			p.ErrorPage(rw, 400, "Bad Request", err.Error())
			return
		}
		preventCaching(rw)
		session := &providers.SessionState{User: user}
		p.SaveSession(rw, req, session)
		http.Redirect(rw, req, redirect, 302)
	} else {
		if p.SkipProviderButton {
			p.OAuthStart(rw, req)
		} else {
			p.SignInPage(rw, req, http.StatusOK)
		}
	}
}

func (p *OAuthProxy) SignOut(rw http.ResponseWriter, req *http.Request) {
	preventCaching(rw)
	p.ClearSessionCookie(rw, req)
	http.Redirect(rw, req, "/", 302)
}

func (p *OAuthProxy) OAuthStart(rw http.ResponseWriter, req *http.Request) {
	preventCaching(rw)
	nonce, err := cookie.Nonce()
	if err != nil {
		p.ErrorPage(rw, 500, "Internal Error", err.Error())
		return
	}
	p.SetCSRFCookie(rw, req, nonce)
	redirect, err := p.GetRedirect(req)
	if err != nil {
		p.ErrorPage(rw, 400, "Bad Request", err.Error())
		return
	}
	redirectURI := p.GetRedirectURI(req.Host)
	state := fmt.Sprintf("%v:%v", nonce, redirect)
	http.Redirect(rw, req, p.provider.GetLoginURL(redirectURI, state), 302)
}

func (p *OAuthProxy) OAuthCallback(rw http.ResponseWriter, req *http.Request) {
	preventCaching(rw)
	remoteAddr := p.getRemoteAddr(req)

	// finish the oauth cycle
	err := req.ParseForm()
	if err != nil {
		p.ErrorPage(rw, 500, "Internal Error", err.Error())
		return
	}
	errorString := req.Form.Get("error")
	if errorString != "" {
		p.ErrorPage(rw, 403, "Permission Denied", errorString)
		return
	}

	session, err := p.redeemCode(req.Host, req.Form.Get("code"))
	if err != nil {
		log.Printf("%s error redeeming code %s", remoteAddr, err)
		p.ErrorPage(rw, 500, "Internal Error", "Internal Error")
		return
	}

	s := strings.SplitN(req.Form.Get("state"), ":", 2)
	if len(s) != 2 {
		p.ErrorPage(rw, 500, "Internal Error", "Invalid State")
		return
	}
	nonce := s[0]
	redirect := s[1]
	c, err := req.Cookie(p.CSRFCookieName)
	if err != nil {
		p.ErrorPage(rw, 403, "Permission Denied", err.Error())
		return
	}
	p.ClearCSRFCookie(rw, req)
	if c.Value != nonce {
		log.Printf("%s csrf token mismatch, potential attack", remoteAddr)
		p.ErrorPage(rw, 403, "Permission Denied", "csrf failed")
		return
	}

	if !p.IsValidRedirect(redirect) {
		redirect = "/"
	}

	// set cookie, or deny
	if p.Validator(session.Email) && p.provider.ValidateGroup(session.Email) {
		log.Printf("%s authentication complete %s", remoteAddr, session)
		err := p.SaveSession(rw, req, session)
		if err != nil {
			log.Printf("%s %s", remoteAddr, err)
			p.ErrorPage(rw, 500, "Internal Error", "Internal Error")
			return
		}
		http.Redirect(rw, req, redirect, 302)
	} else {
		log.Printf("%s Permission Denied: %q is unauthorized", remoteAddr, session.Email)
		p.ErrorPage(rw, 403, "Permission Denied", "Invalid Account")
	}
}

func (p *OAuthProxy) AuthenticateOnly(rw http.ResponseWriter, req *http.Request) {
	// allow caching, do not send no-cache header
	// typically not accessed directly by browsers
	// short caching sometimes useful to prevent multiple simultaneous refreshes
	status := p.Authenticate(rw, req)
	if status == http.StatusAccepted {
		rw.WriteHeader(http.StatusAccepted)
	} else {
		http.Error(rw, "unauthorized request", http.StatusUnauthorized)
	}
}

func (p *OAuthProxy) Proxy(rw http.ResponseWriter, req *http.Request) {
	status := p.Authenticate(rw, req)
	if status == http.StatusInternalServerError {
		p.ErrorPage(rw, http.StatusInternalServerError,
			"Internal Error", "Internal Error")
	} else if status == http.StatusForbidden {
		if p.SkipProviderButton {
			p.OAuthStart(rw, req)
		} else {
			p.SignInPage(rw, req, http.StatusForbidden)
		}
	} else {
		p.serveMux.ServeHTTP(rw, req)
	}
}

func (p *OAuthProxy) Authenticate(rw http.ResponseWriter, req *http.Request) int {
	var saveSession, clearSession, revalidated bool
	remoteAddr := p.getRemoteAddr(req)

	session, sessionAge, err := p.LoadCookiedSession(req)
	if err != nil {
		log.Printf("%s %s", remoteAddr, err)
	}
	if session != nil && p.CookieRefresh != time.Duration(0) && sessionAge > p.CookieRefresh && session.AccessToken != "" {
		log.Printf("%s refreshing %s old session cookie for %s (refresh after %s)", remoteAddr, sessionAge, session, p.CookieRefresh)
		saveSession = true
	}

	if ok, err := p.provider.RefreshSessionIfNeeded(session); err != nil {
		log.Printf("%s removing session. error refreshing access token %s %s", remoteAddr, err, session)
		clearSession = true
		session = nil
	} else if ok {
		saveSession = true
		revalidated = true
	}

	if session != nil && session.IsExpired() {
		log.Printf("%s removing session. token expired %s", remoteAddr, session)
		session = nil
		saveSession = false
		clearSession = true
	}

	if saveSession && !revalidated && session != nil {
		if session.AccessToken != "" {
			if !p.provider.ValidateSessionState(session) {
				log.Printf("%s removing session. error validating %s", remoteAddr, session)
				saveSession = false
				session = nil
				clearSession = true
			}
		} else {
			saveSession = false
		}
	}

	if session != nil && session.Email != "" && !p.Validator(session.Email) {
		log.Printf("%s Permission Denied: removing session %s", remoteAddr, session)
		session = nil
		saveSession = false
		clearSession = true
	}

	if saveSession && session != nil {
		err := p.SaveSession(rw, req, session)
		if err != nil {
			log.Printf("%s %s", remoteAddr, err)
			return http.StatusInternalServerError
		}
	}

	if clearSession {
		p.ClearSessionCookie(rw, req)
	}

	if session == nil {
		session, err = p.CheckBasicAuth(req)
		if err != nil {
			log.Printf("%s %s", remoteAddr, err)
		}
	}

	if session == nil {
		return http.StatusForbidden
	}

	// At this point, the user is authenticated. proxy normally
	if p.PassBasicAuth {
		req.SetBasicAuth(session.User, p.BasicAuthPassword)
	}
	if p.PassUserHeaders {
		req.Header.Set("X-Forwarded-User", session.User)
		if session.Email != "" {
			req.Header.Set("X-Forwarded-Email", session.Email)
		} else {
			req.Header.Del("X-Forwarded-Email")
		}
	}
	if p.SetXAuthRequest {
		rw.Header().Set("X-Auth-Request-User", session.User)
		if session.Email != "" {
			rw.Header().Set("X-Auth-Request-Email", session.Email)
		}
		if p.PassAccessToken && session.AccessToken != "" {
			rw.Header().Set("X-Auth-Request-Access-Token", session.AccessToken)
		}
	}
	if p.PassAccessToken {
		if session.AccessToken != "" {
			req.Header.Set("X-Forwarded-Access-Token", session.AccessToken)
		} else {
			req.Header.Del("X-Forwarded-Access-Token")
		}
	}
	if session.Email == "" {
		rw.Header().Set("GAP-Auth", session.User)
	} else {
		rw.Header().Set("GAP-Auth", session.Email)
	}
	return http.StatusAccepted
}

func (p *OAuthProxy) CheckBasicAuth(req *http.Request) (*providers.SessionState, error) {
	if p.HtpasswdFile == nil {
		return nil, nil
	}
	auth := req.Header.Get("Authorization")
	if auth == "" {
		return nil, nil
	}
	s := strings.SplitN(auth, " ", 2)
	if len(s) != 2 || s[0] != "Basic" {
		return nil, fmt.Errorf("invalid Authorization header %s", req.Header.Get("Authorization"))
	}
	b, err := b64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return nil, err
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return nil, fmt.Errorf("invalid format %s", b)
	}
	if p.HtpasswdFile.Validate(pair[0], pair[1]) {
		log.Printf("authenticated %q via basic auth", pair[0])
		return &providers.SessionState{User: pair[0]}, nil
	}
	return nil, fmt.Errorf("%s not in HtpasswdFile", pair[0])
}
