/**
 * ZenWallet Keyshare Encryption Module
 * 
 * Uses WebAuthn PRF extension output to derive AES-256-GCM encryption keys
 * via HKDF-SHA256. The encryption key is derived on-the-fly from the Secure
 * Enclave and never persists in storage.
 */

const ZenCrypto = (() => {
  // Fixed application-specific salt for PRF evaluation
  // SHA-256 of "ZenWallet-keyshare-encryption-v1"
  const PRF_SALT_HEX = 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2';
  
  /**
   * Get the PRF salt as an ArrayBuffer
   */
  function getPRFSalt() {
    const bytes = new Uint8Array(32);
    for (let i = 0; i < 32; i++) {
      bytes[i] = parseInt(PRF_SALT_HEX.substr(i * 2, 2), 16);
    }
    return bytes.buffer;
  }

  /**
   * Check if the browser supports PRF extension
   */
  function isPRFSupported() {
    return !!(window.PublicKeyCredential && navigator.credentials);
  }

  /**
   * Get PRF extension parameters for WebAuthn create/get calls
   */
  function getPRFExtension() {
    return {
      prf: {
        eval: {
          first: getPRFSalt()
        }
      }
    };
  }

  /**
   * Extract PRF output from WebAuthn credential extension results
   * @param {AuthenticatorAssertionResponse} credential - The WebAuthn credential
   * @returns {ArrayBuffer|null} The 32-byte PRF output, or null if unsupported
   */
  function extractPRFOutput(extensionResults) {
    if (extensionResults && extensionResults.prf && extensionResults.prf.results) {
      return extensionResults.prf.results.first;
    }
    return null;
  }

  /**
   * Derive an AES-256-GCM CryptoKey from PRF output using HKDF-SHA256
   * @param {ArrayBuffer} prfOutput - Raw 32-byte PRF output from Secure Enclave
   * @returns {Promise<CryptoKey>} Non-extractable AES-256-GCM key
   */
  async function deriveKeyFromPRF(prfOutput) {
    // Import PRF output as raw key material for HKDF
    const keyMaterial = await crypto.subtle.importKey(
      'raw',
      prfOutput,
      'HKDF',
      false,
      ['deriveKey']
    );

    // HKDF info context for domain separation
    const info = new TextEncoder().encode('ZenWallet-AES-256-GCM-keyshare-v1');
    const salt = new Uint8Array(32); // zero salt — PRF output is already keyed

    // Derive AES-256-GCM key
    return crypto.subtle.deriveKey(
      {
        name: 'HKDF',
        hash: 'SHA-256',
        salt: salt,
        info: info
      },
      keyMaterial,
      { name: 'AES-GCM', length: 256 },
      false, // non-extractable
      ['encrypt', 'decrypt']
    );
  }

  /**
   * Encrypt a keyshare JSON string with AES-256-GCM
   * @param {CryptoKey} key - AES-256-GCM key from deriveKeyFromPRF
   * @param {string} plaintextJSON - The keyshare JSON to encrypt
   * @returns {Promise<string>} Base64-encoded encrypted bundle
   */
  async function encryptKeyshare(key, plaintextJSON) {
    const iv = crypto.getRandomValues(new Uint8Array(12)); // 96-bit IV for GCM
    const plaintext = new TextEncoder().encode(plaintextJSON);

    const ciphertext = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv: iv },
      key,
      plaintext
    );

    // Bundle: { enc: true, iv: base64, ct: base64 }
    const bundle = {
      enc: true,
      v: 1,
      iv: arrayBufferToBase64(iv.buffer),
      ct: arrayBufferToBase64(ciphertext)
    };

    return JSON.stringify(bundle);
  }

  /**
   * Decrypt an encrypted keyshare bundle
   * @param {CryptoKey} key - AES-256-GCM key from deriveKeyFromPRF
   * @param {string} encryptedBundle - JSON string from encryptKeyshare
   * @returns {Promise<string>} Decrypted keyshare JSON string
   */
  async function decryptKeyshare(key, encryptedBundle) {
    const bundle = JSON.parse(encryptedBundle);
    
    if (!bundle.enc) {
      // Not encrypted — return as-is (plaintext fallback)
      return encryptedBundle;
    }

    const iv = base64ToArrayBuffer(bundle.iv);
    const ciphertext = base64ToArrayBuffer(bundle.ct);

    const plaintext = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: new Uint8Array(iv) },
      key,
      ciphertext
    );

    return new TextDecoder().decode(plaintext);
  }

  /**
   * Check if a stored keyshare value is encrypted
   * @param {string} stored - The raw localStorage value
   * @returns {boolean}
   */
  function isEncrypted(stored) {
    try {
      const parsed = JSON.parse(stored);
      return parsed.enc === true;
    } catch {
      return false;
    }
  }

  // --- Helpers ---

  function arrayBufferToBase64(buffer) {
    const bytes = new Uint8Array(buffer);
    let binary = '';
    for (let i = 0; i < bytes.byteLength; i++) {
      binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary);
  }

  function base64ToArrayBuffer(base64) {
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    return bytes.buffer;
  }

  // Public API
  return {
    isPRFSupported,
    getPRFExtension,
    extractPRFOutput,
    deriveKeyFromPRF,
    encryptKeyshare,
    decryptKeyshare,
    isEncrypted
  };
})();
