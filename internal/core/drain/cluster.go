package drain

import (
	"container/list"
	"strings"
)

type Cluster struct {
	ID             int64
	TemplateTokens []string
	Size           int64
}

func (c *Cluster) Template() string {
	return strings.Join(c.TemplateTokens, " ")
}

// clusterCache indexes clusters by id, optionally bounding their number
// with least-recently-used eviction, mirroring drain3's LogClusterCache:
// lookups do not update the eviction order, only Put and Touch do.
type clusterCache struct {
	maxSize  int
	elements map[int64]*list.Element
	order    *list.List // front = least recently used
}

type cacheEntry struct {
	id      int64
	cluster *Cluster
}

func newClusterCache(maxSize int) *clusterCache {
	return &clusterCache{
		maxSize:  maxSize,
		elements: map[int64]*list.Element{},
		order:    list.New(),
	}
}

func (c *clusterCache) Get(id int64) *Cluster {
	element, exists := c.elements[id]
	if !exists {
		return nil
	}

	return element.Value.(*cacheEntry).cluster
}

func (c *clusterCache) Touch(id int64) {
	if element, exists := c.elements[id]; exists {
		c.order.MoveToBack(element)
	}
}

func (c *clusterCache) Put(id int64, cluster *Cluster) {
	if element, exists := c.elements[id]; exists {
		element.Value.(*cacheEntry).cluster = cluster
		c.order.MoveToBack(element)

		return
	}

	if c.maxSize > 0 && c.order.Len() >= c.maxSize {
		oldest := c.order.Front()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.elements, oldest.Value.(*cacheEntry).id)
		}
	}

	c.elements[id] = c.order.PushBack(&cacheEntry{id: id, cluster: cluster})
}

func (c *clusterCache) Len() int {
	return c.order.Len()
}

// IDs returns the cluster ids from least to most recently used.
func (c *clusterCache) IDs() []int64 {
	ids := make([]int64, 0, c.order.Len())
	for element := c.order.Front(); element != nil; element = element.Next() {
		ids = append(ids, element.Value.(*cacheEntry).id)
	}

	return ids
}

func (c *clusterCache) Values() []*Cluster {
	clusters := make([]*Cluster, 0, c.order.Len())
	for element := c.order.Front(); element != nil; element = element.Next() {
		clusters = append(clusters, element.Value.(*cacheEntry).cluster)
	}

	return clusters
}
