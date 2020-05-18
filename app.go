// Package githubapp provides a convenient interface for handling Github App authentication.
package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/v29/github"
	"golang.org/x/oauth2"
)

// AppsAPI is the interface that is satisfied by the Apps client when authenticated with a JWT.
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -o fakes/fake_apps_api.go . AppsAPI
type AppsAPI interface {
	ListInstallations(ctx context.Context, opt *github.ListOptions) ([]*github.Installation, *github.Response, error)
	CreateInstallationToken(ctx context.Context, id int64, opt *github.InstallationTokenOptions) (*github.InstallationToken, *github.Response, error)
}

// NewClient returns a client for the Github V3 (REST) AppsAPI authenticated with a private key.
func NewClient(integrationID int64, privateKey []byte) (AppsAPI, error) {
	transport, err := ghinstallation.NewAppsTransport(http.DefaultTransport, integrationID, privateKey)
	if err != nil {
		return nil, err
	}
	client := github.NewClient(&http.Client{
		Transport: transport,
	})
	return client.Apps, nil
}

// New returns a new App.
func New(client AppsAPI) *App {
	return &App{
		client:                client,
		installsClientFactory: defaultInstallationsClientFactory,
		updateInterval:        1 * time.Minute,
	}
}

// App wraps the AppsAPI client and caches the installations and repositories for the installation.
type App struct {
	client                AppsAPI
	installs              []*installation
	installsUpdatedAt     time.Time
	installsClientFactory func(string) *github.AppsService
	updateInterval        time.Duration
}

type installation struct {
	ID                    int64
	Owner                 string
	Repositories          []*repository
	RepositoriesUpdatedAt time.Time
}

// repository ...
type repository struct {
	ID   int64
	Name string
}

// CreateInstallationToken returns a new installation token for the given owner, scoped to the provided repositories and permissions.
func (a *App) CreateInstallationToken(owner string, repos []string, permissions *github.InstallationPermissions) (*github.InstallationToken, error) {
	installationID, err := a.getInstallationID(owner)
	if err != nil {
		return nil, err
	}
	tokenOptions := &github.InstallationTokenOptions{
		Permissions: permissions,
	}
	for _, repo := range repos {
		id, err := a.getRepositoryID(owner, repo)
		if err != nil {
			return nil, err
		}
		tokenOptions.RepositoryIDs = append(tokenOptions.RepositoryIDs, id)
	}
	installationToken, _, err := a.client.CreateInstallationToken(context.TODO(), installationID, tokenOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create token: %s", err)
	}
	return installationToken, nil
}

// getInstallation gets the installation ID for the specified owner.
func (a *App) getInstallationID(owner string) (int64, error) {
	if err := a.updateInstallations(); err != nil {
		return 0, err
	}
	for _, i := range a.installs {
		if i.Owner == owner {
			return i.ID, nil
		}
	}
	return 0, ErrInstallationNotFound(owner)
}

// updateInstallations refreshes the installations on a set interval.
func (a *App) updateInstallations() error {
	if a.installsUpdatedAt.Add(a.updateInterval).After(time.Now()) {
		return nil
	}

	var installs []*installation
	var listOptions = &github.ListOptions{PerPage: 10}

	for {
		list, response, err := a.client.ListInstallations(context.TODO(), listOptions)
		if err != nil {
			return err
		}
		for _, i := range list {
			installs = append(installs, &installation{
				ID:    i.GetID(),
				Owner: strings.ToLower(i.Account.GetLogin()),
			})
		}
		if response.NextPage == 0 {
			break
		}
		listOptions.Page = response.NextPage
	}

	a.installs, a.installsUpdatedAt = installs, time.Now()
	return nil
}

// getInstallation gets the repository ID for the repository.
func (a *App) getRepositoryID(owner, repo string) (int64, error) {
	if err := a.updateRepositories(owner); err != nil {
		return 0, err
	}
	for _, i := range a.installs {
		if i.Owner == owner {
			for _, r := range i.Repositories {
				if r.Name == repo {
					return r.ID, nil
				}
			}
		}
	}

	return 0, ErrInstallationNotFound(fmt.Sprintf("%s/%s", owner, repo))
}

// updateRepositories refreshes the list of repositories for the specified owner on a set interval.
func (a *App) updateRepositories(owner string) error {
	var i *installation
	for _, ii := range a.installs {
		if ii.Owner == owner {
			i = ii
		}
	}

	if i.RepositoriesUpdatedAt.Add(a.updateInterval).After(time.Now()) {
		return nil
	}

	token, err := a.CreateInstallationToken(owner, nil, &github.InstallationPermissions{})
	if err != nil {
		return err
	}

	var (
		repositories []*repository
		listOptions  = &github.ListOptions{PerPage: 100}
		client       = a.installsClientFactory(token.GetToken())
	)

	for {
		list, response, err := client.ListRepos(context.TODO(), listOptions)
		if err != nil {
			return err
		}
		for _, r := range list {
			repositories = append(repositories, &repository{
				ID:   r.GetID(),
				Name: r.GetName(),
			})
		}
		if response.NextPage == 0 {
			break
		}
		listOptions.Page = response.NextPage
	}

	i.Repositories, i.RepositoriesUpdatedAt = repositories, time.Now()
	return nil
}

func defaultInstallationsClientFactory(token string) *github.AppsService {
	oauth := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	))
	return github.NewClient(oauth).Apps
}

// ErrInstallationNotFound is returned if the requested App installation is not found.
type ErrInstallationNotFound string

func (e ErrInstallationNotFound) Error() string {
	return fmt.Sprintf("installation not found: '%s'", string(e))
}