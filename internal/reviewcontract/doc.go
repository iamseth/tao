// Package reviewcontract defines the provider-neutral contract for decoding
// structured review output. It owns fenced-block selection, input bounds,
// verdict and approval-proposal policy, and canonical finding normalization.
// Durable review models remain in package plan, while commit proposal validity
// remains owned by package commit.
package reviewcontract
