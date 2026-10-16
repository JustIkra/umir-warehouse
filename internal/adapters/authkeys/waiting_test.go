package authkeys

// waiting is how many callers wait on the refresh in flight.
func (c *Client) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters
}
