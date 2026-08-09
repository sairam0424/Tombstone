import '@testing-library/jest-dom';
import { expect, afterEach, vi } from 'vitest';
import { cleanup, screen } from '@testing-library/react';

// Ensure testing library screen is global
Object.assign(globalThis, { screen });

// Cleanup after each test
afterEach(() => {
  cleanup();
});

// Mock canvas for tests that would use canvas.getContext
// This avoids the jsdom "not implemented" error
if (typeof global.HTMLCanvasElement === 'function') {
  const OriginalCanvasElement = HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.getContext = function(
    contextType: string,
  ): CanvasRenderingContext2D | null {
    if (contextType === '2d') {
      return {
        clearRect: vi.fn(),
        strokeStyle: '',
        lineWidth: 1,
        beginPath: vi.fn(),
        moveTo: vi.fn(),
        lineTo: vi.fn(),
        stroke: vi.fn(),
        fillStyle: '',
        arc: vi.fn(),
        fill: vi.fn(),
        fillText: vi.fn(),
        measureText: vi.fn(() => ({ width: 50 })),
        save: vi.fn(),
        restore: vi.fn(),
        font: '',
        textAlign: 'start',
      } as any;
    }
    return OriginalCanvasElement.call(this, contextType);
  };
}
