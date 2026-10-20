'use strict';
// Keep the reused GM page inside the gateway's own authentication lifecycle.
const gatewayFetch = window.fetch.bind(window);
window.fetch = async (...args) => {
  const response = await gatewayFetch(...args);
  const url = new URL(typeof args[0] === 'string' ? args[0] : args[0].url, location.href);
  if (url.origin === location.origin && (response.status === 401 || (url.pathname === '/api/auth/logout' && response.ok))) {
    location.replace('/admin/');
  }
  return response;
};
