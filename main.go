package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"log/slog"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := _main(); err != nil {
		panic(err)
	}
}

var allowedOwnerKinds = map[string]bool{
	"ReplicaSet":  true,
	"DaemonSet":   true,
	"StatefulSet": true,
}

var NeedUpdateErr = fmt.Errorf("need update")

func _main() error {
	ctx := context.Background()
	clientset, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	repo := NewRepo(egCtx, clientset)

	pods := make(chan *corev1.Pod, 100)
	eg.Go(func() error {
		defer close(pods)
		cont := ""
		for {
			podList, err := clientset.CoreV1().Pods("").List(egCtx, metav1.ListOptions{
				Continue: cont,
				Limit:    16,
			})
			if err != nil {
				return fmt.Errorf("failed to list pods: %w", err)
			}
			for _, pod := range podList.Items {
				select {
				case <-egCtx.Done():
					return nil
				case pods <- &pod:
				}
			}
			cont = podList.Continue
			if cont == "" {
				return nil
			}
		}

	})

	for pod := range pods {
		if pod == nil {
			break
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		owner := pod.ObjectMeta.OwnerReferences
		if len(owner) == 0 {
			continue
		}
		if !allowedOwnerKinds[owner[0].Kind] {
			continue
		}

		containerAlwaysPull := make(map[string]bool)
		for _, container := range pod.Spec.Containers {
			if container.ImagePullPolicy == "Always" {
				containerAlwaysPull[container.Name] = true
			}
		}
		if len(containerAlwaysPull) == 0 {
			continue
		}

		eg.Go(func() error {
			feg := errgroup.Group{}
			for _, status := range pod.Status.ContainerStatuses {
				if !containerAlwaysPull[status.Name] {
					continue
				}
				feg.Go(func() error {
					currentHash := strings.Split(status.ImageID, ":")[1]
					latestHash, err := repo.GetImageDigest(status.Image, pod.Namespace, pod.Spec.ImagePullSecrets)
					if err != nil {
						slog.Warn("failed to get latest image hash for image", "image", status.Image, "error", err)
						return nil
					}
					if currentHash != latestHash {
						return NeedUpdateErr
					}
					return nil
				})
			}
			err = feg.Wait()
			if err == nil {
				return nil
			}
			if !errors.Is(err, NeedUpdateErr) {
				return err
			}
			err = clientset.CoreV1().Pods(pod.Namespace).Delete(egCtx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("failed to delete pod %s/%s: %w", pod.Namespace, pod.Name, err)
			}
			slog.Info("Deleted pod to force image pull", "namespace", pod.Namespace, "name", pod.Name)
			return nil
		})
	}

	return eg.Wait()
}

func getK8sClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local kubeconfig
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
	}
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}
