// workspace-dashboard/src/hooks/useCurrentUser.ts

interface JWTPayload {
  sub?: string;
  email?: string;
  exp?: number;
}

function decodeJWTPayload(token: string): JWTPayload | null {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return null;
    const payload = atob(parts[1].replace(/-/g, '+').replace(/_/g, '/'));
    return JSON.parse(payload) as JWTPayload;
  } catch {
    return null;
  }
}

export function useCurrentUser() {
  // Try localStorage key 'tombstone_token' first, then sessionStorage
  const token =
    (typeof localStorage !== 'undefined' && localStorage.getItem('tombstone_token')) ||
    (typeof sessionStorage !== 'undefined' && sessionStorage.getItem('tombstone_token')) ||
    null;

  if (!token) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  const payload = decodeJWTPayload(token);
  if (!payload) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  // Check expiry
  if (payload.exp && payload.exp * 1000 < Date.now()) {
    return { email: 'anonymous@tombstone.dev', isAuthenticated: false };
  }

  const email = payload.email ?? payload.sub ?? 'anonymous@tombstone.dev';
  return { email, isAuthenticated: true };
}
