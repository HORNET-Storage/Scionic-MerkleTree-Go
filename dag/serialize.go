package dag

import (
	"encoding/json"

	cbor "github.com/fxamacker/cbor/v2"
)

func (dag *Dag) ToCBOR() ([]byte, error) {
	cborData, err := cbor.Marshal(dag)
	if err != nil {
		return nil, err
	}

	return cborData, nil
}

func (dag *Dag) ToJSON() ([]byte, error) {
	jsonData, err := json.MarshalIndent(dag, "", "  ")
	if err != nil {
		return nil, err
	}

	return jsonData, nil
}
