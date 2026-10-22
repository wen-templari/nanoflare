package runner

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http"
)

func (c *Client) PreparePublication(active []nanoflare.ActiveDeployment) (nanoflare.PublicationPreparation, error) {
	var p PrepareResponse
	err := c.request(http.MethodPost, "/v1/generations/prepare", prepareRequest(active), &p)
	return nanoflare.PublicationPreparation{Generation: p.Generation, Deployments: p.Deployments}, err
}
func (c *Client) CommitPublication(p nanoflare.PublicationPreparation) error {
	if err := c.traefik.WriteTraefik(p.Deployments); err != nil {
		return err
	}
	return c.request(http.MethodPost, "/v1/generations/"+p.Generation+"/commit", nil, nil)
}
func (c *Client) AbortPublication(p nanoflare.PublicationPreparation) error {
	return c.request(http.MethodPost, "/v1/generations/"+p.Generation+"/abort", nil, nil)
}
