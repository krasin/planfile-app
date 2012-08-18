// Public Domain (-) 2012 The Planfile App Authors.
// See the Planfile App UNLICENSE file for details.

package main

import (
	"amp/crypto"
	"amp/log"
	"amp/oauth"
	"amp/optparse"
	"amp/runtime"
	"amp/tlsconf"
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

var (
	httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsconf.Config}}
	runPath    string
	tripleDash = []byte("---\n")
)

type Context struct {
	r      *http.Request
	w      http.ResponseWriter
	secret []byte
	secure bool
	token  *oauth.Token
}

func (ctx *Context) Call(path string, v interface{}, body io.Reader) error {
	var (
		err error
		req *http.Request
	)
	if body == nil {
		req, err = http.NewRequest("GET", "https://api.github.com"+path, nil)
	} else {
		req, err = http.NewRequest("POST", "https://api.github.com"+path, body)
	}
	if err != nil {
		return err
	}
	if ctx.token == nil {
		tok, err := hex.DecodeString(ctx.GetCookie("token"))
		if err != nil {
			ctx.ExpireCookie("token")
			return err
		}
		err = json.Unmarshal(tok, ctx.token)
		if err != nil {
			ctx.ExpireCookie("token")
			return err
		}
	}
	req.Header.Add("Authorization", "bearer "+ctx.token.AccessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(v)
	return err
}

func (ctx *Context) Error(s string, err error) {
	log.Error("%s: %s", s, err)
	if err == nil {
		fmt.Fprintf(ctx, "ERROR: %s", s)
	} else {
		fmt.Fprintf(ctx, "ERROR: %s: %s", s, err)
	}
}

func (ctx *Context) ExpireCookie(attr string) {
	http.SetCookie(ctx.w, &http.Cookie{Name: attr, MaxAge: -1, Secure: ctx.secure})
}

func (ctx *Context) FormValue(attr string) string {
	return ctx.r.FormValue(attr)
}

func (ctx *Context) GetCookie(attr string) string {
	cookie, err := ctx.r.Cookie(attr)
	if err != nil {
		return ""
	}
	val, ok := crypto.GetIronValue(attr, cookie.Value, ctx.secret, false)
	if ok {
		return val
	}
	return ""
}

func (ctx *Context) IsAuthorised(repo *Repo) bool {
	auth := ctx.GetCookie("auth")
	if auth == "0" {
		return false
	} else if auth == "1" {
		return true
	}
	user := ctx.GetCookie("user")
	if user == "" {
		ctx.SetCookie("auth", "0")
		return false
	}
	resp, err := httpClient.Get("https://api.github.com/repos/" + repo.Path + "/collaborators/" + user)
	if err != nil {
		log.Error("couldn't do authorisation check for %q: %s", user, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		ctx.SetCookie("auth", "0")
		return false
	}
	ctx.SetCookie("auth", "1")
	return true
}

func (ctx *Context) Redirect(path string) {
	http.Redirect(ctx.w, ctx.r, path, http.StatusFound)
}

func (ctx *Context) SetCookie(attr, val string) {
	http.SetCookie(ctx.w, &http.Cookie{
		Name:     attr,
		Value:    crypto.IronString(attr, val, ctx.secret, -1),
		HttpOnly: true,
		MaxAge:   0,
		Secure:   ctx.secure,
	})
}

func (ctx *Context) SetHeader(attr, val string) {
	ctx.w.Header().Set(attr, val)
}

func (ctx *Context) Write(data []byte) (int, error) {
	return ctx.w.Write(data)
}

func callGithub(path string, v interface{}) error {
	req, err := http.NewRequest("GET", "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(v)
	return err
}

func contains(xs []string, s string) bool {
	for _, e := range xs {
		if e == s {
			return true
		}
	}
	return false
}

func isEqual(x, y []byte) bool {
	if len(x) != len(y) {
		return false
	}
	return subtle.ConstantTimeCompare(x, y) == 1
}

func readFile(path string) []byte {
	c, err := ioutil.ReadFile(path)
	if err != nil {
		runtime.StandardError(err)
	}
	return c
}

func rsplit(s string, sep string) (string, string) {
	i := strings.LastIndex(s, sep)
	if i == -1 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

type Planfile struct {
	Content  string   `json:"content"`
	Depends  []string `json:"depends"`
	Path     string   `json:"path"`
	Rendered string   `json:"rendered"`
	Tags     []string `json:"tags"`
	Title    string   `json:"title"`
}

func NewPlanfile(path string, content []byte) (p *Planfile, id string, section string, users []string, ok bool) {
	var metadata []byte
	if len(content) >= 4 && bytes.HasPrefix(content, tripleDash) {
		s := bytes.SplitN(content[4:], tripleDash, 2)
		if len(s) == 2 {
			metadata = s[0]
			content = bytes.TrimSpace(s[1])
		}
	}
	p = &Planfile{
		Content: string(content),
		Depends: []string{},
		Path:    path,
		Tags:    []string{},
	}
	if len(metadata) > 0 {
		for _, line := range bytes.Split(metadata, []byte{'\n'}) {
			kv := bytes.SplitN(line, []byte{':'}, 2)
			if len(kv) != 2 {
				continue
			}
			v := bytes.TrimSpace(kv[1])
			if len(v) == 0 {
				continue
			}
			switch string(bytes.TrimSpace(kv[0])) {
			case "id":
				id = string(v)
			case "tags":
				for _, f := range bytes.Split(v, []byte{' '}) {
					tag := strings.TrimRight(string(f), ",")
					if len(tag) < 2 {
						continue
					}
					if tag[0] == '@' {
						users = append(users, strings.ToLower(tag[1:]))
					}
					if strings.ToUpper(tag) != tag {
						tag = strings.ToLower(tag)
					}
					if strings.HasPrefix(tag, "dep:") && len(tag) > 4 {
						tag = tag[4:]
						if !contains(p.Depends, tag) {
							p.Depends = append(p.Depends, tag)
						}
					} else {
						if strings.HasSuffix(tag, ":overview") {
							section = tag[:len(tag)-9]
						} else if !contains(p.Tags, tag) {
							p.Tags = append(p.Tags, tag)
						}
					}
				}
			case "title":
				p.Title = string(v)
			}
		}
		sort.StringSlice(p.Depends).Sort()
		sort.StringSlice(p.Tags).Sort()
	}
	rendered, err := renderMarkdown(content)
	if err != nil {
		log.Error("couldn't render %s: %s", path, err)
		return
	}
	ok = true
	p.Rendered = string(rendered)
	return
}

type Repo struct {
	Avatars   map[string]string    `json:"avatars"`
	Ordering  []string             `json:"ordering"`
	Planfiles map[string]*Planfile `json:"planfiles"`
	Sections  map[string]*Planfile `json:"sections"`
	Path      string               `json:"path"`
	TagMap    map[string][]string  `json:"tagmap"`
	Tags      []string             `json:"tags"`
}

func (r *Repo) Load() error {
	log.Info("loading repo: %s", r.Path)
	url := "https://github.com/" + r.Path + "/tarball/master"
	resp, err := httpClient.Get(url)
	if err != nil {
		log.StandardError(err)
		return err
	}
	defer resp.Body.Close()
	zf, err := gzip.NewReader(resp.Body)
	if err != nil {
		log.Error("couldn't find a valid repo tarball at %s -- %s", url, err)
		return err
	}
	tr := tar.NewReader(zf)
	avatars := map[string]string{}
	planfiles := map[string]*Planfile{}
	order := []string{}
	sections := map[string]*Planfile{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Error("reading tarball: %s", err)
			return err
		}
		filename, ext := rsplit(hdr.Name, ".")
		_, filename = rsplit(filename, "/")
		// Check if the file ends with .md and is not a README file.
		if ext == "md" {
			log.Info("parsing: %s", filename)
			data, err := ioutil.ReadAll(tr)
			if err != nil {
				log.Error("reading tarball file %q: %s", hdr.Name, err)
				continue
			}
			pf, id, section, userRefs, ok := NewPlanfile(filename, data)
			if !ok {
				continue
			}
			if strings.ToLower(filename) == "readme" {
				sections["/"] = pf
			} else if section != "" {
				sections[section] = pf
			} else {
				if id == "" {
					log.Error("ID not found for: %s", filename)
					id = filename
				}
				planfiles[id] = pf
			}
			for _, username := range userRefs {
				if _, ok := avatars[username]; !ok {
					user := &User{}
					err = callGithub("/users/"+username, user)
					if err == nil {
						avatars[username] = user.AvatarURL
					} else {
						log.Error("couldn't load github user info for %q: %s", username, err)
						avatars[username] = "https://assets.github.com/images/gravatars/gravatar-140.png"
					}
				}
			}
		} else if ext == "order" && filename == "" {
			log.Info("parsing: .order")
			data, err := ioutil.ReadAll(tr)
			if err != nil {
				log.Error("reading tarball file %q: %s", hdr.Name, err)
				continue
			}
			order = strings.Split(string(bytes.TrimSpace(data)), "\n")
		}
	}
	log.Info("post-processing repo: %s", r.Path)
	tagMap := map[string][]string{}
	for id, f := range planfiles {
		for _, tag := range f.Tags {
			tagMap[tag] = append(tagMap[tag], id)
		}
	}
	for section, _ := range sections {
		if _, ok := tagMap[section]; !ok && section != "/" {
			tagMap[section] = []string{}
		}
	}
	i := 0
	tags := make([]string, len(tagMap))
	for tag, _ := range tagMap {
		tags[i] = tag
		i += 1
	}
	sort.StringSlice(tags).Sort()
	ordering := []string{}
	for _, id := range order {
		if _, ok := planfiles[id]; ok {
			ordering = append(ordering, id)
		}
	}
	extra := []string{}
	for id, _ := range planfiles {
		if !contains(ordering, id) {
			extra = append(extra, id)
		}
	}
	sort.StringSlice(extra).Sort()
	ordering = append(ordering, extra...)
	r.Avatars = avatars
	r.Ordering = ordering
	r.Planfiles = planfiles
	r.Sections = sections
	r.TagMap = tagMap
	r.Tags = tags
	log.Info("successfully loaded repo: %s", r.Path)
	return nil
}

type User struct {
	AvatarURL string `json:"avatar_url"`
	Login     string `json:"login"`
}

func main() {

	// Define the options for the command line and config file options parser.
	opts := optparse.Parser(
		"Usage: planfile <config.yaml> [options]\n",
		"planfile 0.0.1")

	cookieKeyFile := opts.StringConfig("cookie-key-file", "cookie.key",
		"the file containing the key to sign cookie values [cookie.key]")

	gaHost := opts.StringConfig("ga-host", "",
		"the google analytics hostname to use")

	gaID := opts.StringConfig("ga-id", "",
		"the google analytics id to use")

	httpAddr := opts.StringConfig("http-addr", ":8888",
		"the address to bind the http server [:8888]")

	oauthID := opts.StringConfig("oauth-id", "",
		"the oauth client id for github", true)

	oauthSecret := opts.StringConfig("oauth-secret", "",
		"the oauth client secret for github", true)

	redirectURL := opts.StringConfig("redirect-url", "/.oauth",
		"the redirect url for handling oauth [/.oauth]")

	repository := opts.StringConfig("repository", "",
		"the username/repository on github", true)

	secureMode := opts.BoolConfig("secure-mode", false,
		"enable hsts and secure cookies [false]")

	title := opts.StringConfig("title", "Planfile",
		"the title for the web app [Planfile]")

	debug, instanceDirectory, _ := runtime.DefaultOpts("planfile", opts, os.Args)

	service := &oauth.OAuthService{
		ClientID:     *oauthID,
		ClientSecret: *oauthSecret,
		Scope:        "public_repo",
		AuthURL:      "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		RedirectURL:  *redirectURL,
		AcceptHeader: "application/json",
	}

	assets := map[string]string{}
	json.Unmarshal(readFile("assets.json"), &assets)
	runPath = instanceDirectory
	setupPygments()

	mutex := sync.RWMutex{}
	repo := &Repo{Path: *repository}

	err := repo.Load()
	if err != nil {
		runtime.Exit(1)
	}

	repoJSON, err := json.Marshal(repo)
	if err != nil {
		runtime.StandardError(err)
	}

	secret := readFile(*cookieKeyFile)
	register := func(path string, handler func(*Context)) {
		http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			ctx := &Context{
				r:      r,
				w:      w,
				secret: secret,
				secure: *secureMode,
			}
			ctx.SetHeader("Content-Type", "text/html; charset=utf-8")
			handler(ctx)
		})
	}

	anon := []byte(", null, null, false")
	authFalse := []byte("', false")
	authTrue := []byte("', true")

	header := []byte(`<!doctype html>
<meta charset=utf-8>
<title>` + html.EscapeString(*title) + `</title>
<link href="http://fonts.googleapis.com/css?family=Abel|Lato:300,400,700" rel=stylesheet>
<link href=/.static/` + assets["planfile.css"] + ` rel=stylesheet>
<body><script>DATA = ['` + *gaHost + `', '` + *gaID + `', `)

	footer := []byte(`];</script>
<script src=/.static/` + assets["planfile.js"] + `></script>
<noscript>Sorry, your browser needs <a href=http://enable-javascript.com>JavaScript enabled</a>.</noscript>
`)

	register("/", func(ctx *Context) {
		mutex.RLock()
		defer mutex.RUnlock()
		ctx.Write(header)
		ctx.Write(repoJSON)
		avatar := ctx.GetCookie("avatar")
		user := ctx.GetCookie("user")
		if avatar != "" && user != "" {
			ctx.Write([]byte(", '" + user + "', '" + avatar))
			if ctx.IsAuthorised(repo) {
				ctx.Write(authTrue)
			} else {
				ctx.Write(authFalse)
			}
		} else {
			ctx.Write(anon)
		}
		ctx.Write(footer)
	})

	register("/.login", func(ctx *Context) {
		b := make([]byte, 20)
		if n, err := rand.Read(b); err != nil || n != 20 {
			ctx.Error("Couldn't access cryptographic device", err)
			return
		}
		s := hex.EncodeToString(b)
		ctx.SetCookie("state", s)
		ctx.Redirect(service.AuthCodeURL(s))
	})

	register("/.logout", func(ctx *Context) {
		ctx.ExpireCookie("auth")
		ctx.ExpireCookie("avatar")
		ctx.ExpireCookie("token")
		ctx.ExpireCookie("user")
		ctx.Redirect("/")
	})

	register("/.oauth", func(ctx *Context) {
		s := ctx.FormValue("state")
		if s == "" {
			ctx.Redirect("/login")
			return
		}
		if !isEqual([]byte(s), []byte(ctx.GetCookie("state"))) {
			ctx.ExpireCookie("state")
			ctx.Redirect("/login")
			return
		}
		ctx.ExpireCookie("state")
		t := &oauth.Transport{OAuthService: service}
		tok, err := t.ExchangeAuthorizationCode(ctx.FormValue("code"))
		if err != nil {
			ctx.Error("Auth Exchange Error", err)
			return
		}
		jtok, err := json.Marshal(tok)
		if err != nil {
			ctx.Error("Couldn't encode token", err)
			return
		}
		ctx.SetCookie("token", hex.EncodeToString(jtok))
		ctx.token = tok
		user := &User{}
		err = ctx.Call("/user", user, nil)
		if err != nil {
			ctx.Error("Couldn't load user info", err)
			return
		}
		ctx.SetCookie("avatar", user.AvatarURL)
		ctx.SetCookie("user", user.Login)
		ctx.Redirect("/")
	})

	register("/.refresh", func(ctx *Context) {
		mutex.Lock()
		defer mutex.Unlock()
		if !ctx.IsAuthorised(repo) {
			ctx.Write([]byte("ERROR: Not Authorised!"))
			return
		}
		err := repo.Load()
		if err != nil {
			log.Error("couldn't rebuild planfile info: %s", err)
			ctx.Write([]byte("ERROR: " + err.Error()))
			return
		}
		ctx.Redirect("/")
	})

	mimetypes := map[string]string{
		"css":  "text/css",
		"gif":  "image/gif",
		"ico":  "image/x-icon",
		"jpeg": "image/jpeg",
		"jpg":  "image/jpeg",
		"js":   "text/javascript",
		"png":  "image/png",
		"txt":  "text/plain",
	}

	registerStatic := func(filepath, urlpath string) {
		split := strings.Split(filepath, ".")
		ctype, ok := mimetypes[split[len(split)-1]]
		if !ok {
			ctype = "application/octet-stream"
		}
		if debug {
			register(urlpath, func(ctx *Context) {
				ctx.SetHeader("Content-Type", ctype)
				ctx.Write(readFile(filepath))
			})
		} else {
			content := readFile(filepath)
			register(urlpath, func(ctx *Context) {
				ctx.SetHeader("Content-Type", ctype)
				ctx.Write(content)
			})
		}
	}

	for _, path := range assets {
		registerStatic("static/"+path, "/.static/"+path)
	}

	log.Info("Listening on %s", *httpAddr)
	err = http.ListenAndServe(*httpAddr, nil)
	if err != nil {
		runtime.Error("couldn't bind to tcp socket: %s", err)
	}

}
