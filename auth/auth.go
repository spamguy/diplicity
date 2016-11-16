package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aymerick/raymond"
	"github.com/gorilla/mux"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"

	"github.com/zond/go-fcm"
	. "github.com/zond/goaeoas"
	oauth2service "google.golang.org/api/oauth2/v2"
)

var TestMode = false

const (
	LoginRoute          = "Login"
	LogoutRoute         = "Logout"
	RedirectRoute       = "Redirect"
	OAuth2CallbackRoute = "OAuth2Callback"
)

const (
	userKind       = "User"
	naClKind       = "NaCl"
	oAuthKind      = "OAuth"
	userConfigKind = "UserConfig"
	prodKey        = "prod"
)

var (
	prodOAuth     *OAuth
	prodOAuthLock = sync.RWMutex{}
	prodNaCl      *naCl
	prodNaClLock  = sync.RWMutex{}
	router        *mux.Router
)

type FCMNotificationConfig struct {
	ClickActionTemplate string `methods:"PUT"`
	TitleTemplate       string `methods:"PUT"`
	BodyTemplate        string `methods:"PUT"`
}

func (f *FCMNotificationConfig) Customize(ctx context.Context, notif *fcm.NotificationPayload, data interface{}) {
	if customTitle, err := raymond.Render(f.TitleTemplate, data); err == nil {
		notif.Title = customTitle
	} else {
		log.Infof(ctx, "Broken TitleTemplate %q: %v", f.TitleTemplate, err)
	}
	if customBody, err := raymond.Render(f.BodyTemplate, data); err == nil {
		notif.Body = customBody
	} else {
		log.Infof(ctx, "Broken BodyTemplate %q: %v", f.BodyTemplate, err)
	}
	if customClickAction, err := raymond.Render(f.ClickActionTemplate, data); err == nil {
		notif.ClickAction = customClickAction
	} else {
		log.Infof(ctx, "Broken ClickActionTemplate %q: %v", f.ClickActionTemplate, err)
	}
}

func (f *FCMNotificationConfig) Validate() error {
	if f.ClickActionTemplate != "" {
		if _, err := raymond.Parse(f.ClickActionTemplate); err != nil {
			return err
		}
	}
	if f.TitleTemplate != "" {
		if _, err := raymond.Parse(f.TitleTemplate); err != nil {
			return err
		}
	}
	if f.BodyTemplate != "" {
		if _, err := raymond.Parse(f.BodyTemplate); err != nil {
			return err
		}
	}
	return nil
}

type FCMToken struct {
	Value         string                `methods:"PUT"`
	Disabled      bool                  `methods:"PUT"`
	Note          string                `methods:"PUT"`
	App           string                `methods:"PUT"`
	MessageConfig FCMNotificationConfig `methods:"PUT"`
	PhaseConfig   FCMNotificationConfig `methods:"PUT"`
}

type UserConfig struct {
	UserId    string
	FCMTokens []FCMToken `methods:"PUT"`
}

var UserConfigResource = &Resource{
	Load:     loadUserConfig,
	Update:   updateUserConfig,
	FullPath: "/User/{user_id}/UserConfig",
}

func UserConfigID(ctx context.Context, userID *datastore.Key) *datastore.Key {
	return datastore.NewKey(ctx, userConfigKind, "config", 0, userID)
}

func (u *UserConfig) ID(ctx context.Context) *datastore.Key {
	return UserConfigID(ctx, UserID(ctx, u.UserId))
}

func (u *UserConfig) Item(r Request) *Item {
	return NewItem(u).SetName("user-config").
		AddLink(r.NewLink(UserConfigResource.Link("self", Load, []string{"user_id", u.UserId}))).
		AddLink(r.NewLink(UserConfigResource.Link("update", Update, []string{"user_id", u.UserId}))).
		SetDesc([][]string{
		[]string{
			"User configuration",
			"Each diplicity user has exactly one user configuration. User configurations defined user selected configuration for all of diplicty, such as which FCM tokens should be notified of new press or new phases.",
		},
		[]string{
			"FCM tokens",
			"Each FCM token has several field.",
			"A value, which is the registration ID received when registering with FCM.",
			"A disabled flag which will turn notification to that token off, and which the server toggles if FCM returns errors when notifications are sent to that token.",
			"A note field, which the server will populate with the reason the token was disabled.",
			"An app field, which the app populating the token can use to identify tokens belonging to it to avoid removing/updating tokens belonging to other apps.",
		},
		[]string{
			"New phase notifications",
			"New phase notifications will have the `[phase season] [phase year], [phase type]` as title, and `[game desc] has a new phase` as body.",
			"The payload will be `{ DiplicityJSON: DATA }` where DATA is `{ diplicityPhase: [phase JSON], diplicityGame: [game JSON], diplicityUser: [user JSON] }` compressed with libz.",
		},
		[]string{
			"New message notifications",
			"New message notifications will have `[channel members]: Message from [sender]` as title, and `[message body]` as body.",
			"The payload will be `{ DiplicityJSON: DATA }` where DATA is `{ diplicityMessage: [message JSON], diplicityChannel: [channel JSON], diplicityGame: [game JSON] }` compressed with libz.",
		},
	})
}

func loadUserConfig(w ResponseWriter, r Request) (*UserConfig, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*User)
	if !ok {
		return nil, HTTPErr{"unauthorized", 401}
	}

	config := &UserConfig{}
	if err := datastore.Get(ctx, UserConfigID(ctx, user.ID(ctx)), config); err == datastore.ErrNoSuchEntity {
		config.UserId = user.Id
		err = nil
	} else if err != nil {
		return nil, err
	}

	return config, nil
}

func updateUserConfig(w ResponseWriter, r Request) (*UserConfig, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*User)
	if !ok {
		return nil, HTTPErr{"unauthorized", 401}
	}

	config := &UserConfig{}
	if err := Copy(config, r, "PUT"); err != nil {
		return nil, err
	}
	config.UserId = user.Id

	for _, token := range config.FCMTokens {
		if err := token.MessageConfig.Validate(); err != nil {
			return nil, err
		}
		if err := token.PhaseConfig.Validate(); err != nil {
			return nil, err
		}
	}

	if _, err := datastore.Put(ctx, config.ID(ctx), config); err != nil {
		return nil, err
	}

	return config, nil
}

type User struct {
	Email         string
	FamilyName    string
	Gender        string
	GivenName     string
	Hd            string
	Id            string
	Link          string
	Locale        string
	Name          string
	Picture       string
	VerifiedEmail bool
	ValidUntil    time.Time
}

func UserID(ctx context.Context, userID string) *datastore.Key {
	return datastore.NewKey(ctx, userKind, userID, 0, nil)
}

func (u *User) ID(ctx context.Context) *datastore.Key {
	return UserID(ctx, u.Id)
}

func infoToUser(ui *oauth2service.Userinfoplus) *User {
	u := &User{
		Email:      ui.Email,
		FamilyName: ui.FamilyName,
		Gender:     ui.Gender,
		GivenName:  ui.GivenName,
		Hd:         ui.Hd,
		Id:         ui.Id,
		Link:       ui.Link,
		Locale:     ui.Locale,
		Name:       ui.Name,
		Picture:    ui.Picture,
	}
	if ui.VerifiedEmail != nil {
		u.VerifiedEmail = *ui.VerifiedEmail
	}
	return u
}

type naCl struct {
	Secret []byte
}

func getNaClKey(ctx context.Context) *datastore.Key {
	return datastore.NewKey(ctx, naClKind, prodKey, 0, nil)
}

func getNaCl(ctx context.Context) (*naCl, error) {
	// check if in memory
	prodNaClLock.RLock()
	if prodNaCl != nil {
		defer prodNaClLock.RUnlock()
		return prodNaCl, nil
	}
	prodNaClLock.RUnlock()
	// nope, check if in datastore
	prodNaClLock.Lock()
	defer prodNaClLock.Unlock()
	foundNaCl := &naCl{}
	if err := datastore.Get(ctx, getNaClKey(ctx), foundNaCl); err == nil {
		return foundNaCl, nil
	} else if err != datastore.ErrNoSuchEntity {
		return nil, err
	}
	// nope, create new key
	foundNaCl.Secret = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, foundNaCl.Secret); err != nil {
		return nil, err
	}
	// write it transactionally into datastore
	if err := datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		if err := datastore.Get(ctx, getNaClKey(ctx), foundNaCl); err == nil {
			return nil
		} else if err != datastore.ErrNoSuchEntity {
			return err
		}
		if _, err := datastore.Put(ctx, getNaClKey(ctx), foundNaCl); err != nil {
			return err
		}
		return nil
	}, &datastore.TransactionOptions{XG: false}); err != nil {
		return nil, err
	}
	prodNaCl = foundNaCl
	return prodNaCl, nil
}

type OAuth struct {
	ClientID string
	Secret   string
}

func getOAuthKey(ctx context.Context) *datastore.Key {
	return datastore.NewKey(ctx, oAuthKind, prodKey, 0, nil)
}

func SetOAuth(ctx context.Context, oAuth *OAuth) error {
	return datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		currentOAuth := &OAuth{}
		if err := datastore.Get(ctx, getOAuthKey(ctx), currentOAuth); err == nil {
			return HTTPErr{"OAuth already configured", 400}
		}
		if _, err := datastore.Put(ctx, getOAuthKey(ctx), oAuth); err != nil {
			return err
		}
		return nil
	}, &datastore.TransactionOptions{XG: false})
}

func getOAuth(ctx context.Context) (*OAuth, error) {
	prodOAuthLock.RLock()
	if prodOAuth != nil {
		defer prodOAuthLock.RUnlock()
		return prodOAuth, nil
	}
	prodOAuthLock.RUnlock()
	prodOAuthLock.Lock()
	defer prodOAuthLock.Unlock()
	foundOAuth := &OAuth{}
	if err := datastore.Get(ctx, getOAuthKey(ctx), foundOAuth); err != nil {
		return nil, err
	}
	prodOAuth = foundOAuth
	return prodOAuth, nil
}

func getOAuth2Config(ctx context.Context, r Request) (*oauth2.Config, error) {
	scheme := "http"
	if r.Req().TLS != nil {
		scheme = "https"
	}
	redirectURL, err := router.Get(OAuth2CallbackRoute).URL()
	if err != nil {
		return nil, err
	}
	redirectURL.Scheme = scheme
	redirectURL.Host = r.Req().Host

	oauth, err := getOAuth(ctx)
	if err != nil {
		return nil, err
	}

	return &oauth2.Config{
		ClientID:     oauth.ClientID,
		ClientSecret: oauth.Secret,
		RedirectURL:  redirectURL.String(),
		Scopes: []string{
			"openid",
			"profile",
			"email",
		},
		Endpoint: google.Endpoint,
	}, nil
}

func handleLogin(w ResponseWriter, r Request) error {
	ctx := appengine.NewContext(r.Req())

	conf, err := getOAuth2Config(ctx, r)
	if err != nil {
		return err
	}

	loginURL := conf.AuthCodeURL(r.Req().URL.Query().Get("redirect-to"))

	http.Redirect(w, r.Req(), loginURL, 303)
	return nil
}

func encodeToken(ctx context.Context, user *User) (string, error) {
	nacl, err := getNaCl(ctx)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(user)
	if err != nil {
		return "", err
	}
	var nonceAry [24]byte
	if _, err := io.ReadFull(rand.Reader, nonceAry[:]); err != nil {
		return "", err
	}
	var secretAry [32]byte
	copy(secretAry[:], nacl.Secret)
	cipher := secretbox.Seal(nonceAry[:], plain, &nonceAry, &secretAry)
	return base64.URLEncoding.EncodeToString(cipher), nil
}

func handleOAuth2Callback(w ResponseWriter, r Request) error {
	ctx := appengine.NewContext(r.Req())

	conf, err := getOAuth2Config(ctx, r)
	if err != nil {
		return err
	}

	token, err := conf.Exchange(ctx, r.Req().URL.Query().Get("code"))
	if err != nil {
		return err
	}

	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
	service, err := oauth2service.New(client)
	if err != nil {
		return err
	}
	userInfo, err := oauth2service.NewUserinfoService(service).Get().Context(ctx).Do()
	if err != nil {
		return err
	}
	user := infoToUser(userInfo)
	user.ValidUntil = time.Now().Add(time.Hour * 24)
	if _, err := datastore.Put(ctx, datastore.NewKey(ctx, userKind, user.Id, 0, nil), user); err != nil {
		return err
	}

	userToken, err := encodeToken(ctx, user)
	if err != nil {
		return err
	}

	redirectURL, err := url.Parse(r.Req().URL.Query().Get("state"))
	if err != nil {
		return err
	}

	query := url.Values{}
	query.Set("token", userToken)
	redirectURL.RawQuery = query.Encode()

	http.Redirect(w, r.Req(), redirectURL.String(), 303)
	return nil
}

func handleLogout(w ResponseWriter, r Request) error {
	http.Redirect(w, r.Req(), r.Req().URL.Query().Get("redirect-to"), 303)
	return nil
}

func tokenFilter(w ResponseWriter, r Request) (bool, error) {
	if fakeID := r.Req().URL.Query().Get("fake-id"); (TestMode || appengine.IsDevAppServer()) && fakeID != "" {
		user := &User{
			Email:         "fake@fake.fake",
			FamilyName:    "Fakeson",
			GivenName:     "Fakey",
			Id:            fakeID,
			Name:          "Fakey Fakeson",
			VerifiedEmail: true,
			ValidUntil:    time.Now().Add(time.Hour * 24),
		}

		r.Values()["user"] = user

		r.DecorateLinks(func(l *Link, u *url.URL) error {
			if l.Rel != "logout" {
				q := u.Query()
				q.Set("fake-id", fakeID)
				u.RawQuery = q.Encode()
			}
			return nil
		})

		return true, nil
	}

	queryToken := true
	token := r.Req().URL.Query().Get("token")
	if token == "" {
		queryToken = false
		if authHeader := r.Req().Header.Get("Authorization"); authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 {
				return false, HTTPErr{"Authorization header not two parts joined by space", 400}
			}
			if strings.ToLower(parts[0]) != "bearer" {
				return false, HTTPErr{"Authorization header part 1 not 'bearer'", 400}
			}
			token = parts[1]
		}
	}

	if token != "" {
		ctx := appengine.NewContext(r.Req())

		b, err := base64.URLEncoding.DecodeString(token)
		if err != nil {
			return false, err
		}

		var nonceAry [24]byte
		copy(nonceAry[:], b)
		nacl, err := getNaCl(ctx)
		if err != nil {
			return false, err
		}
		var secretAry [32]byte
		copy(secretAry[:], nacl.Secret)

		plain, ok := secretbox.Open([]byte{}, b[24:], &nonceAry, &secretAry)
		if !ok {
			return false, HTTPErr{"badly encrypted token", 403}
		}

		user := &User{}
		if err := json.Unmarshal(plain, user); err != nil {
			return false, err
		}
		if user.ValidUntil.Before(time.Now()) {
			return false, HTTPErr{"token timed out", 401}
		}

		log.Infof(ctx, "Request by %+v", user)

		r.Values()["user"] = user

		if queryToken {
			r.DecorateLinks(func(l *Link, u *url.URL) error {
				if l.Rel != "logout" {
					q := u.Query()
					q.Set("token", token)
					u.RawQuery = q.Encode()
				}
				return nil
			})
		}

	}
	return true, nil
}

func loginRedirect(w ResponseWriter, r Request, errI error) (bool, error) {
	if r.Media() != "text/html" {
		return true, errI
	}

	if herr, ok := errI.(HTTPErr); ok && herr.Status == 401 {
		redirectURL := r.Req().URL
		if r.Req().TLS == nil {
			redirectURL.Scheme = "http"
		} else {
			redirectURL.Scheme = "https"
		}
		redirectURL.Host = r.Req().Host

		loginURL, err := router.Get(LoginRoute).URL()
		if err != nil {
			return false, err
		}
		queryParams := loginURL.Query()
		queryParams.Set("redirect-to", redirectURL.String())
		loginURL.RawQuery = queryParams.Encode()

		http.Redirect(w, r.Req(), loginURL.String(), 307)
		return false, nil
	}

	return true, errI
}

func SetupRouter(r *mux.Router) {
	router = r
	Handle(router, "/Auth/Login", []string{"GET"}, LoginRoute, handleLogin)
	Handle(router, "/Auth/Logout", []string{"GET"}, LogoutRoute, handleLogout)
	Handle(router, "/Auth/OAuth2Callback", []string{"GET"}, OAuth2CallbackRoute, handleOAuth2Callback)
	HandleResource(router, UserConfigResource)
	AddFilter(tokenFilter)
	AddPostProc(loginRedirect)
}
