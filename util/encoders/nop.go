package encoders

/*
	Sliver Implant Framework
	Copyright (C) 2019  Bishop Fox

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

import insecureRand "math/rand"

var (
	MaxN           = 999999999
	EncoderModulus = 101
)

// NopNonce - A NOP nonce identifies a request with no encoder/payload
//
//	any value where mod = 0
func NopNonce() int {
	return insecureRand.Intn(MaxN) * EncoderModulus
}

// NoEncoder - A NOP encoder
type NoEncoder struct{}

// Encode - Don't do anything
func (n NoEncoder) Encode(data []byte) ([]byte, error) {
	return data, nil
}

// Decode - Don't do anything
func (n NoEncoder) Decode(data []byte) ([]byte, error) {
	return data, nil
}
