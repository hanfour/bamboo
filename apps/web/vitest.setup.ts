// SPDX-License-Identifier: AGPL-3.0-or-later

// Registers @testing-library/jest-dom's custom matchers (toBeInTheDocument,
// toHaveAttribute, …) on vitest's expect AND augments its Assertion types,
// so component tests get the matchers with full type-checking. Imported via
// setupFiles in vitest.config.ts.
import '@testing-library/jest-dom/vitest';
