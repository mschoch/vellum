//  Copyright (c) 2017 Couchbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vellum

import "bytes"

// Iterator is a structure for iterating key/value pairs in this FST in
// lexicographic order.  Iterators should be constructed with the Iterator
// method on the parent FST structure.
type Iterator struct {
	f   *FST
	aut Automaton

	startKeyInclusive []byte
	endKeyExclusive   []byte

	statesStack    []fstState
	keysStack      []byte
	keysPosStack   []int
	valsStack      []uint64
	autStatesStack []int
}

func newIterator(f *FST, startKeyInclusive, endKeyExclusive []byte,
	aut Automaton) (*Iterator, error) {

	if aut == nil {
		aut = &AlwaysMatch{}
	}

	rv := &Iterator{
		f:                 f,
		startKeyInclusive: startKeyInclusive,
		endKeyExclusive:   endKeyExclusive,
		aut:               aut,
	}

	err := rv.pointTo(startKeyInclusive)
	if err != nil {
		return nil, err
	}

	return rv, nil
}

// pointTo attempts to point us to the specified location
func (i *Iterator) pointTo(key []byte) error {

	// tried to seek before start
	if bytes.Compare(key, i.startKeyInclusive) < 0 {
		key = i.startKeyInclusive
	}

	// trid to see past end
	if i.endKeyExclusive != nil && bytes.Compare(key, i.endKeyExclusive) > 0 {
		key = i.endKeyExclusive
	}

	// reset any state, pointTo always starts over
	i.statesStack = i.statesStack[:0]
	i.keysStack = i.keysStack[:0]
	i.keysPosStack = i.keysPosStack[:0]
	i.valsStack = i.valsStack[:0]
	i.autStatesStack = i.autStatesStack[:0]

	root, err := i.f.decoder.stateAt(i.f.decoder.getRoot())
	if err != nil {
		return err
	}

	autStart := i.aut.Start()

	maxQ := -1
	// root is always part of the path
	i.statesStack = append(i.statesStack, root)
	i.autStatesStack = append(i.autStatesStack, autStart)
	for j := 0; j < len(key); j++ {
		curr := i.statesStack[len(i.statesStack)-1]
		autCurr := i.autStatesStack[len(i.autStatesStack)-1]

		pos, nextAddr, nextVal := curr.TransitionFor(key[j])
		if nextAddr < 0 {
			// needed transition doesn't exist
			// find last trans before the one we needed
			for q := 0; q < curr.NumTransitions(); q++ {
				if curr.TransitionAt(q) < key[j] {
					maxQ = q
				}
			}
			break
		}
		autNext := i.aut.Accept(autCurr, key[j])

		next, err := i.f.decoder.stateAt(nextAddr)
		if err != nil {
			return err
		}

		i.statesStack = append(i.statesStack, next)
		i.keysStack = append(i.keysStack, key[j])
		i.keysPosStack = append(i.keysPosStack, pos)
		i.valsStack = append(i.valsStack, nextVal)
		i.autStatesStack = append(i.autStatesStack, autNext)
		continue
	}

	if !i.statesStack[len(i.statesStack)-1].Final() || !i.aut.IsMatch(i.autStatesStack[len(i.autStatesStack)-1]) || bytes.Compare(i.keysStack, key) < 0 {
		return i.next(maxQ)
	}

	return nil
}

// Current returns the key and value currently pointed to by the iterator.
// If the iterator is not pointing at a valid value (because Iterator/Next/Seek)
// returned an error previously, it may return nil,0.
func (i *Iterator) Current() ([]byte, uint64) {
	curr := i.statesStack[len(i.statesStack)-1]
	if curr.Final() {
		var total uint64
		for _, v := range i.valsStack {
			total += v
		}
		total += curr.FinalOutput()
		return i.keysStack, total
	}
	return nil, 0
}

// Next advances this iterator to the next key/value pair.  If there is none
// or the advancement goes beyond the configured endKeyExclusive, then
// ErrIteratorDone is returned.
func (i *Iterator) Next() error {
	return i.next(-1)
}

func (i *Iterator) next(lastOffset int) error {

	// remember where we started
	start := make([]byte, len(i.keysStack))
	copy(start, i.keysStack)

	for true {
		curr := i.statesStack[len(i.statesStack)-1]
		autCurr := i.autStatesStack[len(i.autStatesStack)-1]

		if curr.Final() && i.aut.IsMatch(autCurr) &&
			bytes.Compare(i.keysStack, start) > 0 {
			// in final state greater than start key
			return nil
		}

		nextOffset := lastOffset + 1
		if nextOffset < curr.NumTransitions() {
			t := curr.TransitionAt(nextOffset)
			autNext := i.aut.Accept(autCurr, t)
			if i.aut.CanMatch(autNext) {
				pos, nextAddr, v := curr.TransitionFor(t)
				// push onto stack
				next, err := i.f.decoder.stateAt(nextAddr)
				if err != nil {
					return err
				}
				i.statesStack = append(i.statesStack, next)
				i.keysStack = append(i.keysStack, t)
				i.keysPosStack = append(i.keysPosStack, pos)
				i.valsStack = append(i.valsStack, v)
				i.autStatesStack = append(i.autStatesStack, autNext)
				lastOffset = -1

				// check to see if new keystack might have gone too far
				if i.endKeyExclusive != nil && bytes.Compare(i.keysStack, i.endKeyExclusive) >= 0 {
					return ErrIteratorDone
				}
			} else {
				lastOffset = nextOffset
			}

			continue
		}

		if len(i.statesStack) > 1 {
			// no transitions, and still room to pop
			i.statesStack = i.statesStack[:len(i.statesStack)-1]
			i.keysStack = i.keysStack[:len(i.keysStack)-1]
			lastOffset = i.keysPosStack[len(i.keysPosStack)-1]

			i.keysPosStack = i.keysPosStack[:len(i.keysPosStack)-1]
			i.valsStack = i.valsStack[:len(i.valsStack)-1]
			i.autStatesStack = i.autStatesStack[:len(i.autStatesStack)-1]
			continue
		} else {
			// stack len is 1 (root), can't go back further, we're done
			break
		}

	}

	return ErrIteratorDone
}

// Seek advances this iterator to the specified key/value pair.  If this key
// is not in the FST, Current() will return the next largest key.  If this
// seek operation would go past the last key, or outside the configured
// startKeyInclusive/endKeyExclusive then ErrIteratorDone is returned.
func (i *Iterator) Seek(key []byte) error {
	err := i.pointTo(key)
	if err != nil {
		return err
	}
	return nil
}

// Close will free any resources held by this iterator.
func (i *Iterator) Close() error {
	// at the moment we don't do anything, but wanted this for API completeness
	return nil
}
