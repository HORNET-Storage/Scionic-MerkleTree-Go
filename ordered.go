package scionic

// place is where something stands in an order of two numbers: the first
// decides, and the second decides between equals.
type place struct {
	major uint64
	minor uint64
}

func (p place) before(other place) bool {
	return p.major < other.major || (p.major == other.major && p.minor < other.minor)
}

// orderedLevels is how many levels an ordered set has at most. A node is on
// one level more with one chance in four, so this serves far more nodes than
// a list can have items.
const orderedLevels = 20

type orderedNode[V any] struct {
	at    place
	value V
	// next holds, for each level the node is on, the node after it there.
	next []*orderedNode[V]
}

// after is the node that follows in the order, or nil.
func (n *orderedNode[V]) after() *orderedNode[V] {
	return n.next[0]
}

// ordered keeps values by their places, in the order of the places: a skip
// list. Putting, removing and finding take a number of steps that grows
// with the logarithm of how many are kept, and going through them in order
// takes one step each. The zero value is empty.
type ordered[V any] struct {
	head   [orderedLevels]*orderedNode[V]
	length int
	random uint64
}

// link is the link that leads on from node at a level, where a nil node is
// the start.
func (o *ordered[V]) link(node *orderedNode[V], level int) **orderedNode[V] {
	if node == nil {
		return &o.head[level]
	}
	return &node.next[level]
}

// find gives the first node that is not before a place, or nil. With prior,
// it also notes, for each level, the link that leads to the first node there
// that is not before the place.
func (o *ordered[V]) find(at place, prior *[orderedLevels]**orderedNode[V]) *orderedNode[V] {
	var node *orderedNode[V]
	for level := orderedLevels - 1; level >= 0; level-- {
		for {
			next := *o.link(node, level)
			if next == nil || !next.at.before(at) {
				break
			}
			node = next
		}
		if prior != nil {
			prior[level] = o.link(node, level)
		}
	}
	return *o.link(node, 0)
}

// height is how many levels a new node is on: one, and one more with one
// chance in four, again and again.
func (o *ordered[V]) height() int {
	if o.random == 0 {
		o.random = 0x9E3779B97F4A7C15
	}
	o.random ^= o.random << 13
	o.random ^= o.random >> 7
	o.random ^= o.random << 17
	height := 1
	for drawn := o.random; height < orderedLevels && drawn&3 == 0; drawn >>= 2 {
		height++
	}
	return height
}

// put keeps a value at a place, in place of any value that is there.
func (o *ordered[V]) put(at place, value V) {
	var prior [orderedLevels]**orderedNode[V]
	if found := o.find(at, &prior); found != nil && found.at == at {
		found.value = value
		return
	}
	node := &orderedNode[V]{at: at, value: value, next: make([]*orderedNode[V], o.height())}
	for level := range node.next {
		node.next[level] = *prior[level]
		*prior[level] = node
	}
	o.length++
}

// remove forgets what is kept at a place.
func (o *ordered[V]) remove(at place) {
	var prior [orderedLevels]**orderedNode[V]
	found := o.find(at, &prior)
	if found == nil || found.at != at {
		return
	}
	for level := range found.next {
		*prior[level] = found.next[level]
	}
	o.length--
}

// get gives what is kept at a place.
func (o *ordered[V]) get(at place) (V, bool) {
	if found := o.find(at, nil); found != nil && found.at == at {
		return found.value, true
	}
	var none V
	return none, false
}

// first gives the node that comes first in the order, or nil.
func (o *ordered[V]) first() *orderedNode[V] {
	return o.head[0]
}

// len is how many values are kept.
func (o *ordered[V]) len() int {
	return o.length
}
