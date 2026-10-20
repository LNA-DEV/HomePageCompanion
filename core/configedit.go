package core

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

const maskedValue = "••••••"

// putInstanceConfig saves an instance's settings into companion.yaml and
// reloads. The file keeps its comments and every ${VAR} reference: a
// secret field shown masked stays as written, and so does any field whose
// value did not change.
func (c *Core) putInstanceConfig(g *gin.Context) {
	id := g.Param("id")
	var body struct {
		Values map[string]any `json:"values"`
	}
	if err := g.ShouldBindJSON(&body); err != nil {
		g.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	c.mu.RLock()
	in, ok := c.instances[id]
	c.mu.RUnlock()
	if !ok {
		g.JSON(http.StatusNotFound, gin.H{"error": "no such instance"})
		return
	}
	if err := c.editInstance(in, body.Values); err != nil {
		g.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	if err := c.Reload(g.Request.Context()); err != nil {
		g.JSON(http.StatusUnprocessableEntity, gin.H{"error": "saved, but the reload failed: " + err.Error()})
		return
	}
	g.JSON(http.StatusOK, gin.H{"status": "saved"})
}

func (c *Core) editInstance(in *instance, values map[string]any) error {
	if in.service != nil || in.info.New == nil {
		return fmt.Errorf("%s cannot be edited here", in.id)
	}
	secrets := secretFields(reflectTypeOf(in.info.New()))
	changes := map[string]any{}
	for k, v := range values {
		if s, ok := v.(string); ok && s == maskedValue && secrets[k] {
			continue
		}
		if old, ok := in.values[k]; ok && fmt.Sprint(old) == fmt.Sprint(v) {
			continue
		}
		changes[k] = v
	}
	if len(changes) == 0 {
		return nil
	}

	// The new block must still decode: the merged values into a fresh instance.
	merged := map[string]any{}
	for k, v := range in.values {
		merged[k] = v
	}
	for k, v := range changes {
		merged[k] = v
	}
	rest, _, err := splitSlots(in.info, merged)
	if err != nil {
		return err
	}
	if err := decodeConfig(in.info.New(), rest); err != nil {
		return err
	}

	raw, err := os.ReadFile(c.opts.ConfigPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	path := []string{"modules", in.block, "instances", in.name}
	if in.name == companion.DefaultInstance {
		path = []string{"modules", in.block, "config"}
	}
	node := mapPath(doc.Content[0], path)
	for k, v := range changes {
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return err
		}
		setKey(node, k, &vn)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(c.opts.ConfigPath), "."+filepath.Base(c.opts.ConfigPath)+".tmp")
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.opts.ConfigPath)
}

// mapPath walks (and creates) nested mappings.
func mapPath(n *yaml.Node, path []string) *yaml.Node {
	for _, key := range path {
		if n.Kind != yaml.MappingNode {
			n.Kind, n.Tag, n.Value, n.Style = yaml.MappingNode, "!!map", "", 0
			n.Content = nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
		}
		if next.Kind != yaml.MappingNode {
			next.Kind, next.Tag, next.Value, next.Style = yaml.MappingNode, "!!map", "", 0
			next.Content = nil
		}
		n = next
	}
	if n.Style == yaml.FlowStyle && len(n.Content) > 6 {
		n.Style = 0
	}
	return n
}

func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep the comments that belong to the old value.
			v.HeadComment, v.LineComment, v.FootComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment, m.Content[i+1].FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}
