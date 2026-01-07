package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	loader "github.com/abihf/cache-loader"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	regv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PullSecretInfo struct {
	namespace string
	secrets   []corev1.LocalObjectReference
}

type Repo struct {
	ctx           context.Context
	clientset     *kubernetes.Clientset
	pullSecrets   sync.Map
	secretFetcher *loader.Loader[string, DockerAuths]
	digestFetcher *loader.Loader[string, string]
}

func NewRepo(clientset *kubernetes.Clientset) *Repo {
	r := &Repo{
		clientset: clientset,
	}
	r.secretFetcher = loader.New(r.fetchSecret, 0)
	r.digestFetcher = loader.New(r.fetchDigest, 0)
	return r
}

func (r *Repo) GetImageDigest(image string, namespace string, secrets []corev1.LocalObjectReference) (string, error) {
	if len(secrets) > 0 {
		key := getImageWithoutTag(image)
		r.pullSecrets.Store(key, &PullSecretInfo{
			namespace: namespace,
			secrets:   secrets,
		})
	}
	digest, err := r.digestFetcher.Load(image)
	if err != nil {
		return "", err
	}
	return digest, nil
}

func getImageWithoutTag(image string) string {
	if lastColon := strings.LastIndex(image, ":"); lastColon != -1 {
		return image[:lastColon]
	}
	return image
}

func (r *Repo) fetchDigest(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}

	// Get remote image descriptor
	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(r),
		remote.WithPlatform(regv1.Platform{
			Architecture: "amd64",
			OS:           "linux",
		}))
	if err != nil {
		return "", fmt.Errorf("failed to get image descriptor: %w", err)
	}
	return desc.Digest.Hex, nil
}

// Resolve implements [authn.Keychain].
func (r *Repo) Resolve(res authn.Resource) (authn.Authenticator, error) {
	repo := res.String()
	pullSecretIface, ok := r.pullSecrets.Load(repo)
	if !ok {
		return authn.Anonymous, nil
	}
	registry := res.RegistryStr()
	pullSecretInfo := pullSecretIface.(*PullSecretInfo)
	eg := errgroup.Group{}
	for _, secretRef := range pullSecretInfo.secrets {
		secretName := fmt.Sprintf("%s/%s", pullSecretInfo.namespace, secretRef.Name)
		eg.Go(func() error {
			auths, err := r.secretFetcher.Load(secretName)
			if err != nil {
				return err
			}
			if auth, ok := auths[registry]; ok {
				return &foundSecret{auth: auth}
			}
			return nil
		})
	}
	err := eg.Wait()
	if err == nil {
		return authn.Anonymous, nil
	}
	var found *foundSecret
	if ok := errors.As(err, &found); ok {
		return &authn.Basic{
			Username: found.auth.Username,
			Password: found.auth.Password,
		}, nil
	}
	return nil, err
}

type foundSecret struct {
	auth *DockerAuth
}

func (*foundSecret) Error() string {
	return "found secret"
}

func (r *Repo) fetchSecret(ctx context.Context, name string) (DockerAuths, error) {
	splitted := strings.SplitN(name, "/", 2)
	namespace := splitted[0]
	secretName := splitted[1]

	secret, err := r.clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	data, ok := secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain .dockerconfigjson key", namespace, secretName)
	}

	config, err := parseDockerConfigJson(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse docker config json from secret %s/%s: %w", namespace, secretName, err)
	}

	return config.Auths, nil
}

type DockerAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type DockerAuths map[string]*DockerAuth

type DockerConfig struct {
	Auths DockerAuths `json:"auths"`
}

func parseDockerConfigJson(data []byte) (*DockerConfig, error) {
	var config DockerConfig
	err := json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
