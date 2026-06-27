import React, { createContext, useContext, useEffect, useRef, useState } from 'react';
import { TombstoneClient } from '@tomb-stone/core';
import type { TombstoneClientConfig } from '@tomb-stone/core';

interface TombstoneContextValue {
  client: TombstoneClient | null;
  ready: boolean;
}

const TombstoneContext = createContext<TombstoneContextValue>({ client: null, ready: false });

interface TombstoneProviderProps {
  config: TombstoneClientConfig;
  children: React.ReactNode;
  /** For SSR: pre-evaluated flag values from the server, loaded before connecting */
  bootstrapFlags?: Record<string, unknown>;
}

export function TombstoneProvider({ config, children, bootstrapFlags }: TombstoneProviderProps) {
  const [ready, setReady] = useState(false);
  const clientRef = useRef<TombstoneClient | null>(null);

  useEffect(() => {
    const client = new TombstoneClient({
      ...config,
      defaults: { ...bootstrapFlags, ...config.defaults },
    });

    clientRef.current = client;

    client.connect().then(() => {
      setReady(true);
    }).catch(() => {
      // Connect failed — serve from defaults, mark ready so app doesn't hang
      setReady(true);
    });

    return () => {
      client.disconnect();
      clientRef.current = null;
      setReady(false);
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <TombstoneContext.Provider value={{ client: clientRef.current, ready }}>
      {children}
    </TombstoneContext.Provider>
  );
}

export function useTombstoneContext(): TombstoneContextValue {
  return useContext(TombstoneContext);
}
