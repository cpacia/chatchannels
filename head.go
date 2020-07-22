package channels

import (
	"encoding/json"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
)

type Head []byte

func (h *Head) GetLinks() ([]cid.Cid, error) {
	if len(*h) == 0 {
		return nil, nil
	}
	var s []string
	if err := json.Unmarshal(*h, &s); err != nil {
		return nil, err
	}
	ret := make([]cid.Cid, 0, len(s))
	for _, cidStr := range s {
		id, err := cid.Decode(cidStr)
		if err != nil {
			return nil, err
		}
		ret = append(ret, id)
	}
	return ret, nil
}

func (h *Head) SetHead(ids []cid.Cid) error {
	s := make([]string, 0, len(ids))
	for _, id := range ids {
		s = append(s, id.String())
	}
	out, err := json.MarshalIndent(s, "", "    ")
	if err != nil {
		return err
	}
	*h = Head(out)
	return nil
}

func (h *Head) UpdateHead(nd ipld.Node) error {
	ids, err := h.GetLinks()
	if err != nil {
		return err
	}
	idMap := make(map[cid.Cid]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	for _, link := range nd.Links() {
		if idMap[link.Cid] {
			delete(idMap, link.Cid)
		}
	}

	newState := make([]string, 0, len(idMap)+1)
	if !idMap[nd.Cid()] {
		newState = append(newState, nd.Cid().String())
	}
	for id := range idMap {
		newState = append(newState, id.String())
		if len(idMap) >= 49 {
			break
		}
	}

	out, err := json.MarshalIndent(newState, "", "    ")
	if err != nil {
		return err
	}
	*h = Head(out)
	return nil
}
