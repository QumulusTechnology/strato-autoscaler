package stratocloud

import (
	"time"

	"github.com/imroc/req/v3"
)

type Client struct {
	httpCli *req.Client
}

type ClientOpts struct {
	Host       string
	ApiToken   string
	Timeout    time.Duration
	TenantName string
	TenantID   string
	Username   string
	Password   string
}

func NewClient(opts ClientOpts) *Client {
	httpCli := req.C().
		DevMode().
		SetBaseURL(opts.Host).
		EnableInsecureSkipVerify().
		SetCommonHeaders(map[string]string{
			"x-api-token":       opts.ApiToken,
			"x-os-project-name": opts.TenantName,
			"x-os-project-id":   opts.TenantID,
			"x-os-username":     opts.Username,
			"x-os-password":     opts.Password,
		}).
		SetTimeout(opts.Timeout)

	return &Client{
		httpCli: httpCli,
	}
}
