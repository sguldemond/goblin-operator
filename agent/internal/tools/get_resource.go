package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type GetResource struct {
	dynCli dynamic.Interface
	mapper meta.RESTMapper
}

func NewGetResource(dynCli dynamic.Interface, mapper meta.RESTMapper) *GetResource {
	return &GetResource{dynCli: dynCli, mapper: mapper}
}

func (t *GetResource) Name() string { return "getResource" }

func (t *GetResource) Description() string {
	return "Fetch or list any Kubernetes resource. " +
		"Provide name for a single resource, omit name to list. " +
		"Supports labelSelector and fieldSelector when listing. " +
		"Use this for Pods, Deployments, Nodes, ResourceQuotas, Events, ReplicaSets, etc."
}

func (t *GetResource) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"apiVersion":    {"type": "string", "description": "e.g. apps/v1, v1, batch/v1"},
			"kind":          {"type": "string", "description": "e.g. Pod, Deployment, Node, Event"},
			"name":          {"type": "string", "description": "Resource name. Omit to list."},
			"namespace":     {"type": "string", "description": "Namespace. Omit for cluster-scoped resources."},
			"labelSelector": {"type": "string", "description": "Label selector when listing, e.g. app=foo"},
			"fieldSelector": {"type": "string", "description": "Field selector when listing, e.g. involvedObject.name=mypod"}
		},
		"required": ["apiVersion", "kind"]
	}`)
}

type getResourceParams struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"labelSelector"`
	FieldSelector string `json:"fieldSelector"`
}

func (t *GetResource) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p getResourceParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	gv, err := schema.ParseGroupVersion(p.APIVersion)
	if err != nil {
		return "", fmt.Errorf("invalid apiVersion %q: %w", p.APIVersion, err)
	}
	mapping, err := t.mapper.RESTMapping(gv.WithKind(p.Kind).GroupKind(), gv.Version)
	if err != nil {
		return "", fmt.Errorf("unknown resource %s/%s: %w", p.APIVersion, p.Kind, err)
	}

	clusterScoped := mapping.Scope.Name() == meta.RESTScopeNameRoot
	ri := t.dynCli.Resource(mapping.Resource)

	var result any
	if p.Name != "" {
		if clusterScoped {
			result, err = ri.Get(ctx, p.Name, metav1.GetOptions{})
		} else {
			result, err = ri.Namespace(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		}
	} else {
		listOpts := metav1.ListOptions{
			LabelSelector: p.LabelSelector,
			FieldSelector: p.FieldSelector,
		}
		if clusterScoped {
			result, err = ri.List(ctx, listOpts)
		} else {
			result, err = ri.Namespace(p.Namespace).List(ctx, listOpts)
		}
	}
	if err != nil {
		return "", fmt.Errorf("getting %s: %w", p.Kind, err)
	}

	b, err := json.MarshalIndent(stripNoise(result), "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// stripNoise removes verbose fields that clutter the output.
func stripNoise(obj any) any {
	b, _ := json.Marshal(obj)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return obj
	}
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
		if ann, ok := md["annotations"].(map[string]any); ok {
			delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}
	// Strip noise from list items too.
	if items, ok := m["items"].([]any); ok {
		for _, item := range items {
			if im, ok := item.(map[string]any); ok {
				if md, ok := im["metadata"].(map[string]any); ok {
					delete(md, "managedFields")
				}
			}
		}
	}
	return m
}

var _ Tool = (*GetResource)(nil)
